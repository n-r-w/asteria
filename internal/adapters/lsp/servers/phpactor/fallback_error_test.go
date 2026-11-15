package lspphpactor

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/domain"
)

// fallbackFixture holds a class whose method read reads the property value on line 3.
const fallbackFixture = "<?php\n" +
	"class Fixture {\n" +
	"    public $value = 1;\n" +
	"    public function read() { return $this->value; }\n" +
	"}\n"

// fallbackErrorSuite checks which errors of a phpactor fallback step end the request.
type fallbackErrorSuite struct {
	suite.Suite
}

// TestFallbackError runs the checks of fallback step errors.
func TestFallbackError(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(fallbackErrorSuite))
}

// TestOtherErrorKeepsPrimaryResult checks that an ordinary error of the property reference fallback leaves the
// references of the language server in the result.
func (s *fallbackErrorSuite) TestOtherErrorKeepsPrimaryResult() {
	service, request := s.newService(
		fixtureFiles(),
		fixtureAnswer(func() error { return errors.New("document symbols are not available") }),
		nil,
		nil,
	)

	result, err := service.FindReferencingSymbols(s.T().Context(), request)
	s.Require().NoError(err)

	s.Require().Len(result.Symbols, 1)
	s.Equal("Fixture/read", result.Symbols[0].Path)
	s.Empty(result.Warnings)
}

// TestEndedRequestContextEndsRequest checks that a fallback error after the request context ended ends the request.
func (s *fallbackErrorSuite) TestEndedRequestContextEndsRequest() {
	ctx, cancel := context.WithCancel(s.T().Context())
	service, request := s.newService(fixtureFiles(), fixtureAnswer(func() error {
		cancel()

		return errors.New("document symbols are canceled")
	}), nil, nil)

	_, err := service.FindReferencingSymbols(ctx, request)
	// The error is either the ended context or the fallback error, depending on which one the client sees first.
	s.Error(err)
}

// TestUnavailableSessionEndsRequest checks that a fallback step without a language server session ends the request.
func (s *fallbackErrorSuite) TestUnavailableSessionEndsRequest() {
	sessionErr := errors.New("server stopped")
	service, request := s.newService(fixtureFiles(), fixtureAnswer(nil), sessionErr, nil)

	_, err := service.FindReferencingSymbols(s.T().Context(), request)
	s.ErrorIs(err, sessionErr)
}

// TestTargetFileUsageFailureKeepsOtherFallbackReferences checks that a property usage in the target file that the
// fallback cannot process names the target file in the warning, while the usages of other files stay.
func (s *fallbackErrorSuite) TestTargetFileUsageFailureKeepsOtherFallbackReferences() {
	files := fixtureFiles()
	files["b.php"] = usageFixture("B")
	files["c.php"] = usageFixture("C")
	service, request := s.newService(files, func(method string, documentPath string, call int) (any, error) {
		fileName := filepath.Base(documentPath)
		switch method {
		case protocol.MethodTextDocumentDocumentSymbol:
			// The third request of fixture.php reads the container of its usage, after the target lookup of the
			// language server and of the fallback.
			if fileName == "fixture.php" && call == 3 {
				return nil, errors.New("document symbols are not available")
			}
			if fileName == "fixture.php" {
				return fixtureSymbols(), nil
			}

			return usageSymbols(strings.ToUpper(strings.TrimSuffix(fileName, ".php"))), nil
		case protocol.MethodTextDocumentReferences:
			return []protocol.Location{}, nil
		default:
			return nil, errors.New("method is not supported by the test language server")
		}
	}, nil, []phpactorReferenceRow{{File: "b.php", Line: 2}, {File: "fixture.php", Line: 3}, {File: "c.php", Line: 2}})

	result, err := service.FindReferencingSymbols(s.T().Context(), request)
	s.Require().NoError(err)

	referencingFiles := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		referencingFiles = append(referencingFiles, symbol.File)
	}
	s.Equal([]string{"b.php", "c.php"}, referencingFiles)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "fixture.php")
}

// testAnswer answers one language server request of a test about the file at documentPath; call counts the
// requests of this method for this file from 1.
type testAnswer func(method string, documentPath string, call int) (any, error)

// fixtureFiles returns fixture.php with the class Fixture.
func fixtureFiles() map[string]string {
	return map[string]string{"fixture.php": fallbackFixture}
}

// fixtureAnswer answers the first documentSymbol request with the class tree and later ones, which come from the
// fallback, with the error of fallbackSymbols when it is set; it answers references with the usage of the
// property in the method read.
func fixtureAnswer(fallbackSymbols func() error) testAnswer {
	return func(method string, documentPath string, call int) (any, error) {
		switch method {
		case protocol.MethodTextDocumentDocumentSymbol:
			if call > 1 && fallbackSymbols != nil {
				if err := fallbackSymbols(); err != nil {
					return nil, err
				}
			}

			return fixtureSymbols(), nil
		case protocol.MethodTextDocumentReferences:
			return []protocol.Location{{URI: uri.File(documentPath), Range: fixtureRange(3, 43, 3, 48)}}, nil
		default:
			return nil, errors.New("method is not supported by the test language server")
		}
	}
}

// newService writes files into a new workspace and creates a phpactor service whose language server is an
// in-memory peer that answers through answer, and whose member-reference command returns propertyRows.
// sessionErr, when set, ends the session after the references request.
func (s *fallbackErrorSuite) newService(
	files map[string]string,
	answer testAnswer,
	sessionErr error,
	propertyRows []phpactorReferenceRow,
) (*Service, *domain.FindReferencingSymbolsRequest) {
	t := s.T()
	workspaceRoot := t.TempDir()
	for name, content := range files {
		s.Require().NoError(os.WriteFile(filepath.Join(workspaceRoot, name), []byte(content), 0o600))
	}

	var referenced atomic.Bool
	conn := s.newConn(func(method string, documentPath string, call int) (any, error) {
		if method == protocol.MethodTextDocumentReferences {
			referenced.Store(true)
		}

		return answer(method, documentPath, call)
	})

	rt, err := runtimelsp.New(&runtimelsp.RuntimeConfig{
		Command:                 phpactorServerName,
		Args:                    nil,
		ServerName:              phpactorServerName,
		ShutdownTimeout:         0,
		ReplyConfiguration:      nil,
		BuildClientCapabilities: nil,
		FileWatch:               nil,
		PatchInitializeParams:   nil,
		HandleServerCallback:    nil,
		AfterInitialized:        nil,
		WaitUntilReady:          nil,
		BuildWorkspaceFolders:   nil,
		SessionIdleTimeout:      0,
	})
	s.Require().NoError(err)
	t.Cleanup(func() {
		s.NoError(rt.Close(context.WithoutCancel(t.Context())))
	})

	withRequestDocument := helpers.WithRequestDocument(func(string) string { return phpLanguageID })
	ensureConn := func(context.Context, string) (jsonrpc2.Conn, error) {
		if sessionErr != nil && referenced.Load() {
			return nil, sessionErr
		}

		return conn, nil
	}
	std, err := stdlsp.New(&stdlsp.Config{
		Extensions:                   extensions,
		EnsureConn:                   ensureConn,
		WithRequestDocument:          withRequestDocument,
		OpenFileForDocumentSymbol:    true,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    shouldIgnoreDir,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	})
	s.Require().NoError(err)

	return &Service{
		rt:                  rt,
		ensureConn:          ensureConn,
		std:                 std,
		withRequestDocument: withRequestDocument,
		runPropertyReferences: func(context.Context, string, string, string) ([]phpactorReferenceRow, error) {
			return propertyRows, nil
		},
		cacheRoot: t.TempDir(),
	}, &domain.FindReferencingSymbolsRequest{
		Path:          "Fixture/value",
		IncludeKinds:  nil,
		ExcludeKinds:  nil,
		WorkspaceRoot: workspaceRoot,
		File:          "fixture.php",
	}
}

// newConn builds an in-memory language server that accepts document notifications and answers each request
// through answer.
func (s *fallbackErrorSuite) newConn(answer testAnswer) jsonrpc2.Conn {
	t := s.T()
	var (
		mu    sync.Mutex
		calls = make(map[string]int)
	)
	serverSide, clientSide := net.Pipe()
	serverConn := jsonrpc2.NewConn(jsonrpc2.NewStream(serverSide))
	clientConn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	serverConn.Go(t.Context(), func(_ context.Context, req *jsonrpc2.Request) (any, error) {
		switch req.Method() {
		case protocol.MethodTextDocumentDidOpen, protocol.MethodTextDocumentDidClose:
			//nolint:nilnil // Notifications get no reply, so the handler returns no result.
			return nil, nil
		default:
			var params struct {
				TextDocument protocol.TextDocumentIdentifier `json:"textDocument"`
			}
			if err := json.Unmarshal(req.Params(), &params); err != nil {
				return nil, err
			}
			documentPath := params.TextDocument.URI.FsPath()
			method := req.Method()
			mu.Lock()
			calls[method+" "+documentPath]++
			call := calls[method+" "+documentPath]
			mu.Unlock()

			return answer(method, documentPath, call)
		}
	})
	clientConn.Go(t.Context(), jsonrpc2.MethodNotFoundHandler)
	t.Cleanup(func() {
		s.NoError(clientConn.Close())
		s.NoError(serverConn.Close())
		for _, done := range []<-chan struct{}{clientConn.Done(), serverConn.Done()} {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for the test language server to shut down")
			}
		}
	})

	return clientConn
}

// fixtureSymbols returns the document symbols of fallbackFixture.
func fixtureSymbols() []protocol.DocumentSymbol {
	return []protocol.DocumentSymbol{{
		Name:           "Fixture",
		Detail:         nil,
		Kind:           protocol.SymbolKindClass,
		Tags:           nil,
		Deprecated:     nil,
		Range:          fixtureRange(1, 0, 4, 1),
		SelectionRange: fixtureRange(1, 6, 1, 13),
		Children: []protocol.DocumentSymbol{
			{
				Name:           "value",
				Detail:         nil,
				Kind:           protocol.SymbolKindProperty,
				Tags:           nil,
				Deprecated:     nil,
				Range:          fixtureRange(2, 4, 2, 22),
				SelectionRange: fixtureRange(2, 12, 2, 17),
				Children:       nil,
			},
			{
				Name:           "read",
				Detail:         nil,
				Kind:           protocol.SymbolKindMethod,
				Tags:           nil,
				Deprecated:     nil,
				Range:          fixtureRange(3, 4, 3, 51),
				SelectionRange: fixtureRange(3, 20, 3, 24),
				Children:       nil,
			},
		},
	}}
}

// usageFixture returns a file with the class className whose method useIt reads the property value on line 2.
func usageFixture(className string) string {
	return "<?php\n" +
		"class " + className + " {\n" +
		"    public function useIt(Fixture $fixture) { return $fixture->value; }\n" +
		"}\n"
}

// usageSymbols returns the document symbols of usageFixture for the class className.
func usageSymbols(className string) []protocol.DocumentSymbol {
	return []protocol.DocumentSymbol{{
		Name:           className,
		Detail:         nil,
		Kind:           protocol.SymbolKindClass,
		Tags:           nil,
		Deprecated:     nil,
		Range:          fixtureRange(1, 0, 3, 1),
		SelectionRange: fixtureRange(1, 6, 1, 7),
		Children: []protocol.DocumentSymbol{{
			Name:           "useIt",
			Detail:         nil,
			Kind:           protocol.SymbolKindMethod,
			Tags:           nil,
			Deprecated:     nil,
			Range:          fixtureRange(2, 4, 2, 71),
			SelectionRange: fixtureRange(2, 20, 2, 25),
			Children:       nil,
		}},
	}}
}

// fixtureRange builds one LSP range.
func fixtureRange(startLine, startCharacter, endLine, endCharacter uint32) protocol.Range {
	return protocol.Range{
		Start: protocol.Position{Line: startLine, Character: startCharacter},
		End:   protocol.Position{Line: endLine, Character: endCharacter},
	}
}
