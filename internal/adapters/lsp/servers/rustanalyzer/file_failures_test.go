package lsprustanalyzer

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
	"github.com/n-r-w/asteria/internal/domain"
)

// rustFixture holds the struct Thing with the field run on line 0 and its impl with the method run on line 1.
const rustFixture = "struct Thing { run: i32 }\nimpl Thing { fn run() {} }\n"

// fileFailureSuite checks that a file that fails in one attempt of a rust-analyzer search stays out of the result.
type fileFailureSuite struct {
	suite.Suite
}

// TestFileFailures runs the rust-analyzer checks of failed files.
func TestFileFailures(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(fileFailureSuite))
}

// TestRawSymbolRetryLeavesOutFileFailedInFirstAttempt checks that a file that fails in the canonical attempt keeps
// its warning and gets no symbols from the raw impl-path attempt.
func (s *fileFailureSuite) TestRawSymbolRetryLeavesOutFileFailedInFirstAttempt() {
	env := s.newEnv([]string{"a.rs", "b.rs"}, func(method string, fileName string, call int) (any, error) {
		if method != protocol.MethodTextDocumentDocumentSymbol {
			return nil, errors.New("method is not supported by the test language server")
		}
		if fileName == "a.rs" && call == 1 {
			return nil, errors.New("document symbols are not available")
		}

		return []protocol.DocumentSymbol{implSymbol()}, nil
	}, "")

	result, err := env.service.FindSymbol(s.T().Context(), &domain.FindSymbolRequest{
		Path:              "Thing/run",
		IncludeKinds:      nil,
		ExcludeKinds:      nil,
		Depth:             0,
		IncludeBody:       false,
		IncludeInfo:       false,
		SubstringMatching: false,
		WorkspaceRoot:     env.workspaceRoot,
		Scope:             "",
	})
	s.Require().NoError(err)

	files := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		files = append(files, symbol.File)
	}
	s.Equal([]string{"b.rs"}, files)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "a.rs")
}

// TestRawReferenceRetryIgnoresRowsOfFileFailedInSameAttempt checks that rows of a file that fails later in the
// canonical attempt do not suppress the raw impl-path attempt.
func (s *fileFailureSuite) TestRawReferenceRetryIgnoresRowsOfFileFailedInSameAttempt() {
	// Line 99 is outside x.rs, so x.rs fails after its first row.
	env := s.newEnv(
		referenceFiles(),
		referenceAnswers([][]testReference{{{"x.rs", 0}, {"x.rs", 99}}, {{"y.rs", 0}}}),
		"",
	)

	result, err := env.service.FindReferencingSymbols(s.T().Context(), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"y.rs"}, referencingFiles(result))
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "x.rs")
}

// TestRawReferenceRetryIgnoresRowsOfSkippedNeighbor checks that rows of a neighbor that could not be opened do not
// suppress the raw impl-path attempt.
func (s *fileFailureSuite) TestRawReferenceRetryIgnoresRowsOfSkippedNeighbor() {
	env := s.newEnv(referenceFiles(), referenceAnswers([][]testReference{{{"x.rs", 0}}, {{"y.rs", 0}}}), "x.rs")

	result, err := env.service.FindReferencingSymbols(s.T().Context(), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"y.rs"}, referencingFiles(result))
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "x.rs")
}

// TestRawReferenceRetryKeepsFailureOfFirstAttempt checks that a file that fails in the canonical attempt keeps its
// warning and gets no rows from the raw impl-path attempt.
func (s *fileFailureSuite) TestRawReferenceRetryKeepsFailureOfFirstAttempt() {
	// Line 99 is outside x.rs, so x.rs fails in the canonical attempt without rows.
	env := s.newEnv(
		referenceFiles(),
		referenceAnswers([][]testReference{{{"x.rs", 99}}, {{"x.rs", 0}, {"y.rs", 0}}}),
		"",
	)

	result, err := env.service.FindReferencingSymbols(s.T().Context(), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"y.rs"}, referencingFiles(result))
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "x.rs")
}

// testAnswer answers one language server request of a test about fileName; call counts the requests of this method
// for this file from 1.
type testAnswer func(method string, fileName string, call int) (any, error)

// testEnv holds a rust-analyzer service whose language server is an in-memory peer that answers from a test script.
type testEnv struct {
	service       *Service
	workspaceRoot string
}

// referenceRequest builds the reference search for the canonical path of the method run in t.rs.
func (e *testEnv) referenceRequest() *domain.FindReferencingSymbolsRequest {
	return &domain.FindReferencingSymbolsRequest{
		Path:          "Thing/run",
		IncludeKinds:  nil,
		ExcludeKinds:  nil,
		WorkspaceRoot: e.workspaceRoot,
		File:          "t.rs",
	}
}

// testReference is one reference location that the test language server returns.
type testReference struct {
	file string
	line uint32
}

// referenceFiles returns the target t.rs and its neighbors x.rs and y.rs.
func referenceFiles() []string {
	return []string{"t.rs", "x.rs", "y.rs"}
}

// referenceAnswers answers documentSymbol with the struct and the impl of rustFixture, and the n-th references
// request with the locations of attempts[n]; later requests get the last answer.
func referenceAnswers(attempts [][]testReference) testAnswer {
	return func(method string, _ string, call int) (any, error) {
		switch method {
		case protocol.MethodTextDocumentDocumentSymbol:
			return []protocol.DocumentSymbol{structSymbol(), implSymbol()}, nil
		case protocol.MethodTextDocumentReferences:
			return attempts[min(call, len(attempts))-1], nil
		default:
			return nil, errors.New("method is not supported by the test language server")
		}
	}
}

// newEnv writes rustFixture into each file of a new workspace and creates a rust-analyzer service whose language
// server answers through answer. Opening the file failOpen, when set, fails.
func (s *fileFailureSuite) newEnv(files []string, answer testAnswer, failOpen string) *testEnv {
	t := s.T()
	workspaceRoot := t.TempDir()
	for _, file := range files {
		s.Require().NoError(os.WriteFile(filepath.Join(workspaceRoot, file), []byte(rustFixture), 0o600))
	}
	conn := s.newConn(workspaceRoot, answer)

	rt, err := runtimelsp.New(&runtimelsp.RuntimeConfig{
		Command:                 rustAnalyzerServerName,
		Args:                    nil,
		ServerName:              rustAnalyzerServerName,
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

	ensureConn := func(context.Context, string) (jsonrpc2.Conn, error) {
		return conn, nil
	}
	baseWithRequestDocument := helpers.WithRequestDocument(func(string) string { return rustLanguageID })
	withRequestDocument := func(
		ctx context.Context,
		conn jsonrpc2.Conn,
		absolutePath string,
		run func(context.Context) error,
	) error {
		if failOpen != "" && filepath.Base(absolutePath) == failOpen {
			return errors.New("document cannot be opened")
		}

		return baseWithRequestDocument(ctx, conn, absolutePath, run)
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
	stdReferences, err := stdlsp.New(&stdlsp.Config{
		Extensions:                   extensions,
		EnsureConn:                   ensureConn,
		WithRequestDocument:          nil,
		OpenFileForDocumentSymbol:    false,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    shouldIgnoreDir,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	})
	s.Require().NoError(err)

	service := newTestService(t.TempDir(), cfgadapters.RustAnalyzerConfig{
		WorkspaceSymbolSearchLimit: 0,
		StartupReadyTimeout:        0,
	})
	service.rt = rt
	service.ensureConn = ensureConn
	service.std = std
	service.stdReferences = stdReferences
	service.withRequestDocument = withRequestDocument

	return &testEnv{service: service, workspaceRoot: workspaceRoot}
}

// newConn builds an in-memory language server that accepts document notifications and answers each request
// through answer; it turns test references into locations of files of workspaceRoot.
func (s *fileFailureSuite) newConn(workspaceRoot string, answer testAnswer) jsonrpc2.Conn {
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
			fileName := filepath.Base(params.TextDocument.URI.FsPath())
			method := req.Method()
			mu.Lock()
			calls[method+" "+fileName]++
			call := calls[method+" "+fileName]
			mu.Unlock()

			answered, err := answer(method, fileName, call)
			if references, ok := answered.([]testReference); ok {
				return locations(workspaceRoot, references), err
			}

			return answered, err
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

// locations converts test references into locations of the field run in files of workspaceRoot.
func locations(workspaceRoot string, references []testReference) []protocol.Location {
	result := make([]protocol.Location, 0, len(references))
	for _, reference := range references {
		result = append(result, protocol.Location{
			URI:   uri.File(filepath.Join(workspaceRoot, reference.file)),
			Range: fixtureRange(reference.line, 15, reference.line, 18),
		})
	}

	return result
}

// structSymbol returns the struct Thing of rustFixture with its field run.
func structSymbol() protocol.DocumentSymbol {
	return fixtureSymbol("Thing", protocol.SymbolKindStruct, 0, fixtureSymbol("run", protocol.SymbolKindField, 0))
}

// implSymbol returns the impl of Thing in rustFixture with its method run.
func implSymbol() protocol.DocumentSymbol {
	return fixtureSymbol("impl Thing", protocol.SymbolKindObject, 1, fixtureSymbol("run", protocol.SymbolKindMethod, 1))
}

// fixtureSymbol builds one symbol that spans the given line of rustFixture.
func fixtureSymbol(
	name string,
	kind protocol.SymbolKind,
	line uint32,
	children ...protocol.DocumentSymbol,
) protocol.DocumentSymbol {
	return protocol.DocumentSymbol{
		Name:           name,
		Detail:         nil,
		Kind:           kind,
		Tags:           nil,
		Deprecated:     nil,
		Range:          fixtureRange(line, 0, line, 25),
		SelectionRange: fixtureRange(line, 15, line, 18),
		Children:       children,
	}
}

// referencingFiles returns the file of each row of a reference search result in order.
func referencingFiles(result domain.FindReferencingSymbolsResult) []string {
	files := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		files = append(files, symbol.File)
	}

	return files
}

// fixtureRange builds one LSP range.
func fixtureRange(startLine, startCharacter, endLine, endCharacter uint32) protocol.Range {
	return protocol.Range{
		Start: protocol.Position{Line: startLine, Character: startCharacter},
		End:   protocol.Position{Line: endLine, Character: endCharacter},
	}
}
