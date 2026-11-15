package stdlsp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/n-r-w/asteria/internal/domain"
)

// fileFailureSuite checks which per-file errors a search reports as warnings and which end the search.
type fileFailureSuite struct {
	suite.Suite

	workspaceRoot string
	// handleHover answers hover requests of the test language server.
	handleHover func() (any, error)
	// referenceLocations is the answer of the test language server to each references request.
	referenceLocations []protocol.Location
	conn               jsonrpc2.Conn
}

// TestFileFailures runs the per-file failure checks of symbol and reference searches.
func TestFileFailures(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(fileFailureSuite))
}

// SetupTest creates three files and a language server that returns one symbol for each document.
func (s *fileFailureSuite) SetupTest() {
	s.workspaceRoot = s.T().TempDir()
	for _, name := range []string{"a.ts", "b.ts", "c.ts"} {
		s.Require().NoError(os.WriteFile(filepath.Join(s.workspaceRoot, name), []byte("export const x = 1;\n"), 0o600))
	}
	s.handleHover = func() (any, error) {
		return map[string]any{"contents": map[string]any{"kind": "plaintext", "value": "info"}}, nil
	}
	s.referenceLocations = nil
	s.conn = s.newConn()
}

// TestDirectorySearchReturnsOtherFilesAndNamesFailedFile checks that a failed file becomes a warning.
func (s *fileFailureSuite) TestDirectorySearchReturnsOtherFilesAndNamesFailedFile() {
	service := s.newService(failingDocument("b.ts", errors.New("broken file")), nil)

	result, err := service.FindSymbol(s.T().Context(), s.request(""))
	s.Require().NoError(err)

	s.Len(result.Symbols, 2)
	for _, symbol := range result.Symbols {
		s.NotEqual("b.ts", symbol.File)
	}
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "b.ts: ")
	s.Contains(result.Warnings[0], "broken file")
}

// TestFileSearchReturnsError checks that a search over one file keeps returning the error of this file.
func (s *fileFailureSuite) TestFileSearchReturnsError() {
	service := s.newService(failingDocument("b.ts", errors.New("broken file")), nil)

	_, err := service.FindSymbol(s.T().Context(), s.request("b.ts"))
	s.ErrorContains(err, "broken file")
}

// TestEndedRequestContextEndsSearch checks that an ended request context ends the search.
func (s *fileFailureSuite) TestEndedRequestContextEndsSearch() {
	ctx, cancel := context.WithCancel(s.T().Context())
	withDocument := func(
		callCtx context.Context,
		_ jsonrpc2.Conn,
		absolutePath string,
		run func(context.Context) error,
	) error {
		if filepath.Base(absolutePath) == "b.ts" {
			cancel()

			return callCtx.Err()
		}

		return run(callCtx)
	}
	service := s.newService(withDocument, nil)

	_, err := service.FindSymbol(ctx, s.request(""))
	s.ErrorIs(err, context.Canceled)
}

// TestUnavailableSessionEndsSearch checks that a failed language server session ends the search.
func (s *fileFailureSuite) TestUnavailableSessionEndsSearch() {
	sessionErr := errors.New("server does not start")
	service := s.newService(failingDocument("", nil), func(int32) error { return sessionErr })

	_, err := service.FindSymbol(s.T().Context(), s.request(""))
	s.ErrorIs(err, sessionErr)
}

// TestHoverAfterEndedRequestContextEndsSearch checks that symbol info does not hide an ended request context.
func (s *fileFailureSuite) TestHoverAfterEndedRequestContextEndsSearch() {
	ctx, cancel := context.WithCancel(s.T().Context())
	var hoverCalls atomic.Int32
	// The context ends during the hover of the last file, so no later file request can notice it.
	s.handleHover = func() (any, error) {
		if hoverCalls.Add(1) < 3 {
			return map[string]any{"contents": map[string]any{"kind": "plaintext", "value": "info"}}, nil
		}
		cancel()

		return nil, errors.New("hover is canceled")
	}
	service := s.newService(failingDocument("", nil), nil)
	request := s.request("")
	request.IncludeInfo = true

	_, err := service.FindSymbol(ctx, request)
	// The error is either the ended context or the hover error, depending on which one the client sees first.
	s.Error(err)
}

// TestHoverWithoutSessionEndsSearch checks that symbol info does not hide a failed language server session.
func (s *fileFailureSuite) TestHoverWithoutSessionEndsSearch() {
	sessionErr := errors.New("server stopped")
	// Each file takes one call for documentSymbol and one for hover, so the sixth call serves the hover of the
	// last file and no later file request can notice the failed session.
	service := s.newService(failingDocument("", nil), func(call int32) error {
		if call == 6 {
			return sessionErr
		}

		return nil
	})
	request := s.request("")
	request.IncludeInfo = true

	_, err := service.FindSymbol(s.T().Context(), request)
	s.ErrorIs(err, sessionErr)
}

// TestReferenceSearchLeavesOutAllRowsOfFailedFile checks that a file whose later reference fails keeps no rows.
func (s *fileFailureSuite) TestReferenceSearchLeavesOutAllRowsOfFailedFile() {
	s.referenceLocations = []protocol.Location{
		s.location("b.ts", 0),
		s.location("b.ts", 99),
		s.location("c.ts", 0),
	}
	service := s.newService(failingDocument("", nil), nil)

	result, err := service.FindReferencingSymbols(s.T().Context(), &domain.FindReferencingSymbolsRequest{
		Path:          "fixtureSymbol",
		IncludeKinds:  nil,
		ExcludeKinds:  nil,
		WorkspaceRoot: s.workspaceRoot,
		File:          "a.ts",
	})
	s.Require().NoError(err)

	s.Require().Len(result.Symbols, 1)
	s.Equal("c.ts", result.Symbols[0].File)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "1 file ")
	s.Contains(result.Warnings[0], "b.ts: ")
}

// TestReferenceSearchLeavesOutResolvedTargetFileThatFails checks that a target file that fails after its symbol was
// resolved keeps no rows and is named in the warning, while the references of other files stay.
func (s *fileFailureSuite) TestReferenceSearchLeavesOutResolvedTargetFileThatFails() {
	// Line 99 is outside a.ts, so the target file fails after its first row.
	s.referenceLocations = []protocol.Location{
		s.location("b.ts", 0),
		s.location("a.ts", 0),
		s.location("a.ts", 99),
		s.location("c.ts", 0),
	}
	service := s.newService(failingDocument("", nil), nil)

	result, err := service.FindReferencingSymbols(s.T().Context(), &domain.FindReferencingSymbolsRequest{
		Path:          "fixtureSymbol",
		IncludeKinds:  nil,
		ExcludeKinds:  nil,
		WorkspaceRoot: s.workspaceRoot,
		File:          "a.ts",
	})
	s.Require().NoError(err)

	files := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		files = append(files, symbol.File)
	}
	s.Equal([]string{"b.ts", "c.ts"}, files)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "a.ts: ")
}

// TestReferenceWorkflowTargetCloseErrorKeepsOtherReferences checks that a target file that stays open for the whole
// reference workflow and fails only to close afterwards is named in the warning without rows, while the references
// of other files stay.
func (s *fileFailureSuite) TestReferenceWorkflowTargetCloseErrorKeepsOtherReferences() {
	s.referenceLocations = []protocol.Location{s.location("b.ts", 0), s.location("a.ts", 0), s.location("c.ts", 0)}
	closeErr := errors.New("close request document: notification failed")
	// The first request of a.ts is the workflow document, which closes after all other requests finished.
	var targetOpens atomic.Int32
	withDocument := func(
		ctx context.Context,
		_ jsonrpc2.Conn,
		absolutePath string,
		run func(context.Context) error,
	) error {
		workflowDocument := filepath.Base(absolutePath) == "a.ts" && targetOpens.Add(1) == 1
		runErr := run(ctx)
		if workflowDocument {
			return errors.Join(runErr, closeErr)
		}

		return runErr
	}
	service, err := New(&Config{
		Extensions: []string{".ts"},
		EnsureConn: func(context.Context, string) (jsonrpc2.Conn, error) {
			return s.conn, nil
		},
		WithRequestDocument:          withDocument,
		OpenFileForDocumentSymbol:    true,
		OpenFileForReferenceWorkflow: true,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	})
	s.Require().NoError(err)

	result, err := service.FindReferencingSymbols(s.T().Context(), &domain.FindReferencingSymbolsRequest{
		Path:          "fixtureSymbol",
		IncludeKinds:  nil,
		ExcludeKinds:  nil,
		WorkspaceRoot: s.workspaceRoot,
		File:          "a.ts",
	})
	s.Require().NoError(err)

	files := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		files = append(files, symbol.File)
	}
	s.Equal([]string{"b.ts", "c.ts"}, files)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "a.ts: ")
}

// TestCollectorCountsBothSeparatorFormsAsOneFile checks that one file named with either separator counts once.
func (s *fileFailureSuite) TestCollectorCountsBothSeparatorFormsAsOneFile() {
	failures := NewFileFailures(s.workspaceRoot, false)

	s.Require().NoError(failures.Record(s.T().Context(), `sub\bad.php`, errors.New("broken file")))
	s.Require().NoError(failures.Record(s.T().Context(), "sub/bad.php", errors.New("broken file")))
	result := domain.FindReferencingSymbolsResult{
		Symbols: []domain.ReferencingSymbol{{
			Kind:             12,
			Path:             "Use",
			File:             `sub\bad.php`,
			ContentStartLine: 0,
			ContentEndLine:   0,
			Content:          "",
		}},
		Incomplete: false,
		Warnings:   nil,
	}
	failures.CompleteReferenceResult(&result)

	s.Empty(result.Symbols)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "1 file ")
}

// TestSymbolResultLeavesOutAllRowsOfFailedFile checks that a file that failed after some of its symbols were
// added keeps no symbols in the result.
func (s *fileFailureSuite) TestSymbolResultLeavesOutAllRowsOfFailedFile() {
	failures := NewFileFailures(s.workspaceRoot, false)
	result := domain.FindSymbolResult{
		Symbols: []domain.FoundSymbol{
			{Kind: 13, Body: "", Info: "", Path: "AliasOne", File: "reexports.ts", StartLine: 0, EndLine: 0},
			{Kind: 13, Body: "", Info: "", Path: "Other", File: "other.ts", StartLine: 0, EndLine: 0},
		},
		Warnings: nil,
	}

	s.Require().NoError(failures.Record(s.T().Context(), "reexports.ts", errors.New("alias is broken")))
	failures.CompleteSymbolResult(&result)

	s.Require().Len(result.Symbols, 1)
	s.Equal("other.ts", result.Symbols[0].File)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "reexports.ts: alias is broken")
}

// TestReasonNamesFileOnce checks that a reason drops the wrappers that only repeat the file and keeps the failed
// operation and its cause.
func (s *fileFailureSuite) TestReasonNamesFileOnce() {
	failures := NewFileFailures(s.workspaceRoot, false)
	cause := errors.New("jsonrpc2: marshaling notify parameters: bad")
	err := fmt.Errorf(
		"request document symbols: %w",
		fmt.Errorf("open request document %q: %w", filepath.Join(s.workspaceRoot, "b.ts"), cause),
	)

	s.Require().NoError(failures.Record(s.T().Context(), "b.ts", err))
	result := domain.FindSymbolResult{Symbols: nil, Warnings: nil}
	failures.CompleteSymbolResult(&result)

	s.Require().Len(result.Warnings, 1)
	s.Equal(1, strings.Count(result.Warnings[0], "b.ts"), result.Warnings[0])
	s.Contains(result.Warnings[0], "open request document")
	s.Contains(result.Warnings[0], cause.Error())
}

// newService builds the service through New so the configuration hooks behave as in adapters.
// ensureConnErr returns the error of the given 1-based EnsureConn call, or nil.
func (s *fileFailureSuite) newService(
	withDocument WithRequestDocumentFunc,
	ensureConnErr func(call int32) error,
) *Service {
	var calls atomic.Int32
	service, err := New(&Config{
		Extensions: []string{".ts"},
		EnsureConn: func(context.Context, string) (jsonrpc2.Conn, error) {
			call := calls.Add(1)
			if ensureConnErr != nil {
				if callErr := ensureConnErr(call); callErr != nil {
					return nil, callErr
				}
			}

			return s.conn, nil
		},
		WithRequestDocument:          withDocument,
		OpenFileForDocumentSymbol:    true,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	})
	s.Require().NoError(err)

	return service
}

// request builds a search for the symbol that the test language server returns.
func (s *fileFailureSuite) request(scope string) *domain.FindSymbolRequest {
	return &domain.FindSymbolRequest{
		Path:              "fixtureSymbol",
		IncludeKinds:      nil,
		ExcludeKinds:      nil,
		Depth:             0,
		IncludeBody:       false,
		IncludeInfo:       false,
		SubstringMatching: false,
		WorkspaceRoot:     s.workspaceRoot,
		Scope:             scope,
	}
}

// location builds a reference location at the start of the given line of one workspace file.
func (s *fileFailureSuite) location(name string, line uint32) protocol.Location {
	return protocol.Location{
		URI: uri.File(filepath.Join(s.workspaceRoot, name)),
		Range: protocol.Range{
			Start: protocol.Position{Line: line, Character: 13},
			End:   protocol.Position{Line: line, Character: 14},
		},
	}
}

// newConn builds an in-memory language server that returns one symbol for each document, the hover answer of
// handleHover, and referenceLocations for each references request.
func (s *fileFailureSuite) newConn() jsonrpc2.Conn {
	t := s.T()
	serverSide, clientSide := net.Pipe()
	serverConn := jsonrpc2.NewConn(jsonrpc2.NewStream(serverSide))
	clientConn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	serverConn.Go(t.Context(), func(ctx context.Context, req *jsonrpc2.Request) (any, error) {
		switch req.Method() {
		case protocol.MethodTextDocumentDocumentSymbol:
			return []map[string]any{{
				"name":           "fixtureSymbol",
				"kind":           int(protocol.SymbolKindConstant),
				"range":          testRange(0, 0, 0, 20),
				"selectionRange": testRange(0, 13, 0, 14),
			}}, nil
		case protocol.MethodTextDocumentHover:
			return s.handleHover()
		case protocol.MethodTextDocumentReferences:
			return s.referenceLocations, nil
		default:
			return jsonrpc2.MethodNotFoundHandler(ctx, req)
		}
	})
	clientConn.Go(t.Context(), jsonrpc2.MethodNotFoundHandler)

	t.Cleanup(func() {
		require.NoError(t, clientConn.Close())
		require.NoError(t, serverConn.Close())
		waitForConnDone(t, "clientConn", clientConn.Done())
		waitForConnDone(t, "serverConn", serverConn.Done())
	})

	return clientConn
}

// testRange builds one LSP range.
func testRange(startLine, startCharacter, endLine, endCharacter uint32) protocol.Range {
	return protocol.Range{
		Start: protocol.Position{Line: startLine, Character: startCharacter},
		End:   protocol.Position{Line: endLine, Character: endCharacter},
	}
}

// failingDocument returns err for the file with the given base name and runs the request for other files.
func failingDocument(baseName string, err error) WithRequestDocumentFunc {
	return func(ctx context.Context, _ jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		if filepath.Base(absolutePath) == baseName {
			return err
		}

		return run(ctx)
	}
}
