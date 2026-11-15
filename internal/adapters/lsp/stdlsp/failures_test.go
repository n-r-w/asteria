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
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
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
	// secondSymbol makes the test language server return the symbol fixtureSymbolTwo on line 1 of each document after
	// fixtureSymbol.
	secondSymbol bool
	conn         jsonrpc2.Conn
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
	s.secondSymbol = false
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

// TestDeadlineMidWalkReturnsProcessedFiles checks that a directory search that reaches the deadline returns the
// symbols of the files processed before the stop and counts the files that were not searched.
func (s *fileFailureSuite) TestDeadlineMidWalkReturnsProcessedFiles() {
	service := s.newService(waitForDeadline("b.ts", 0), nil)

	result, err := service.FindSymbol(s.deadlineContext(), s.request(""))
	s.Require().NoError(err)

	s.Equal([]string{"a.ts"}, symbolFiles(result))
	s.Equal([]string{".ts: 2 files are not searched: search stopped by timeout"}, result.Warnings)
}

// TestDeadlineDuringHoverReturnsProcessedFiles checks that a deadline during symbol info also stops the search with
// the symbols of the files processed before the stop.
func (s *fileFailureSuite) TestDeadlineDuringHoverReturnsProcessedFiles() {
	// The first request of b.ts reads its symbols, and the second one asks for the hover of its symbol.
	service := s.newService(waitForDeadline("b.ts", 1), nil)
	request := s.request("")
	request.IncludeInfo = true

	result, err := service.FindSymbol(s.deadlineContext(), request)
	s.Require().NoError(err)

	s.Equal([]string{"a.ts"}, symbolFiles(result))
	s.Equal([]string{".ts: 2 files are not searched: search stopped by timeout"}, result.Warnings)
}

// TestDeadlineAfterFileLoadStartsNoLaterFile checks that a directory search whose deadline passes when the symbols of
// one file have loaded starts no document request for a later file, and counts the later files as not searched.
func (s *fileFailureSuite) TestDeadlineAfterFileLoadStartsNoLaterFile() {
	var requestedFiles []string
	service := s.newService(recordDocumentRequests(finishAfterDeadline("a.ts", 1), &requestedFiles), nil)

	result, err := service.FindSymbol(s.deadlineContext(), s.request(""))
	s.Require().NoError(err)

	s.Equal([]string{"a.ts"}, symbolFiles(result))
	s.Equal([]string{".ts: 2 files are not searched: search stopped by timeout"}, result.Warnings)
	s.Equal([]string{"a.ts"}, requestedFiles)
}

// TestDeadlineAfterHoverBuildsNoLaterSymbol checks that a directory search whose deadline passes when the info of one
// symbol has arrived keeps this symbol, starts no document request for the next symbol of the same file, and counts
// the file and the later files as not searched.
func (s *fileFailureSuite) TestDeadlineAfterHoverBuildsNoLaterSymbol() {
	s.secondSymbol = true
	var requestedFiles []string
	// The first document request of a.ts reads its symbols, and the second one asks for the info of fixtureSymbol.
	service := s.newService(recordDocumentRequests(finishAfterDeadline("a.ts", 2), &requestedFiles), nil)
	request := s.request("")
	request.Path = "fixtureSymbol"
	request.SubstringMatching = true
	request.IncludeInfo = true

	result, err := service.FindSymbol(s.deadlineContext(), request)
	s.Require().NoError(err)

	s.Require().Len(result.Symbols, 1)
	s.Equal("fixtureSymbol", result.Symbols[0].Path)
	s.Equal([]string{".ts: 3 files are not searched: search stopped by timeout"}, result.Warnings)
	s.Equal([]string{"a.ts", "a.ts"}, requestedFiles)
}

// TestDeadlineBeforeFirstFileReturnsEmptyResult checks that a search whose deadline passed before the first file
// returns no symbols and counts all files as not searched.
func (s *fileFailureSuite) TestDeadlineBeforeFirstFileReturnsEmptyResult() {
	service := s.newService(failingDocument("", nil), nil)

	result, err := service.FindSymbol(s.expiredContext(), s.request(""))
	s.Require().NoError(err)

	s.Empty(result.Symbols)
	s.Equal([]string{".ts: 3 files are not searched: search stopped by timeout"}, result.Warnings)
}

// TestDeadlineKeepsWarningOfFailedFile checks that a search stopped by the deadline names the failed files in their
// own warning and counts the one file that was not searched.
func (s *fileFailureSuite) TestDeadlineKeepsWarningOfFailedFile() {
	waitAtC := waitForDeadline("c.ts", 0)
	service := s.newService(func(
		ctx context.Context,
		conn jsonrpc2.Conn,
		absolutePath string,
		run func(context.Context) error,
	) error {
		if filepath.Base(absolutePath) == "a.ts" {
			return errors.New("broken file")
		}

		return waitAtC(ctx, conn, absolutePath, run)
	}, nil)

	result, err := service.FindSymbol(s.deadlineContext(), s.request(""))
	s.Require().NoError(err)

	s.Equal([]string{"b.ts"}, symbolFiles(result))
	s.Require().Len(result.Warnings, 2)
	s.Contains(result.Warnings[0], "a.ts: ")
	s.Equal(".ts: 1 file is not searched: search stopped by timeout", result.Warnings[1])
}

// TestDeadlineEndsFileSearch checks that a search over one file returns the error of the deadline.
func (s *fileFailureSuite) TestDeadlineEndsFileSearch() {
	service := s.newService(failingDocument("", nil), nil)

	_, err := service.FindSymbol(s.expiredContext(), s.request("b.ts"))
	s.ErrorIs(err, context.DeadlineExceeded)
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

	result, err := service.FindReferencingSymbols(s.T().Context(), s.referenceRequest())
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

	result, err := service.FindReferencingSymbols(s.T().Context(), s.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"b.ts", "c.ts"}, referencingFiles(result))
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "a.ts: ")
}

// TestReferenceWorkflowTargetCloseErrorKeepsAllReferences checks that a target file that stays open for the whole
// reference workflow and fails only to send didClose afterwards keeps its rows, and that no warning names it.
func (s *fileFailureSuite) TestReferenceWorkflowTargetCloseErrorKeepsAllReferences() {
	s.referenceLocations = []protocol.Location{s.location("b.ts", 0), s.location("a.ts", 0), s.location("c.ts", 0)}
	withRequestDocument := helpers.WithRequestDocument(func(string) string { return "typescript" })
	// The first request of a.ts is the workflow document, which closes after all other requests finished.
	var targetOpens atomic.Int32
	withDocument := func(
		ctx context.Context,
		conn jsonrpc2.Conn,
		absolutePath string,
		run func(context.Context) error,
	) error {
		workflowDocument := filepath.Base(absolutePath) == "a.ts" && targetOpens.Add(1) == 1

		return withRequestDocument(ctx, conn, absolutePath, func(callCtx context.Context) error {
			runErr := run(callCtx)
			if workflowDocument {
				// A closed connection makes the didClose notification of the workflow document fail.
				s.Require().NoError(conn.Close())
			}

			return runErr
		})
	}
	service := s.newReferenceWorkflowService(withDocument)

	result, err := service.FindReferencingSymbols(s.T().Context(), s.referenceRequest())
	s.Require().NoError(err)

	s.ElementsMatch([]string{"a.ts", "b.ts", "c.ts"}, referencingFiles(result))
	s.Empty(result.Warnings)
}

// TestReferenceDeadlineReturnsProcessedFiles checks that a reference search that reaches the deadline while it
// processes the referencing files returns the references of the files processed before the stop, and counts once
// each file of the rest of the location list, including the file at which the search stopped.
func (s *fileFailureSuite) TestReferenceDeadlineReturnsProcessedFiles() {
	s.referenceLocations = []protocol.Location{
		s.location("b.ts", 0),
		s.location("c.ts", 0),
		s.location("a.ts", 0),
		s.location("c.ts", 0),
	}
	service := s.newService(waitForDeadline("c.ts", 0), nil)

	result, err := service.FindReferencingSymbols(s.deadlineContext(), s.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"b.ts"}, referencingFiles(result))
	s.Equal([]string{".ts: 2 files are not processed: search stopped by timeout"}, result.Warnings)
}

// TestReferenceDeadlineDuringLocationRequestEndsSearch checks that a reference search whose deadline passes before
// the language server returns the reference locations returns the error of the deadline.
func (s *fileFailureSuite) TestReferenceDeadlineDuringLocationRequestEndsSearch() {
	s.referenceLocations = []protocol.Location{s.location("b.ts", 0)}
	// The first request of a.ts reads the target symbol, and the second one asks for the reference locations.
	service := s.newService(waitForDeadline("a.ts", 1), nil)

	_, err := service.FindReferencingSymbols(s.deadlineContext(), s.referenceRequest())
	s.ErrorIs(err, context.DeadlineExceeded)
}

// TestReferenceDeadlineBeforeTargetLocationStopsProcessing checks that a reference search whose deadline passes after
// the location list arrived stops before the first location, also when it is in the target file whose symbols are
// already loaded, and counts each file of the list as not processed.
func (s *fileFailureSuite) TestReferenceDeadlineBeforeTargetLocationStopsProcessing() {
	s.referenceLocations = []protocol.Location{s.location("a.ts", 0), s.location("b.ts", 0)}
	// The first request of a.ts reads the target symbol, and the second one asks for the reference locations.
	service := s.newService(finishAfterDeadline("a.ts", 2), nil)

	result, err := service.FindReferencingSymbols(s.deadlineContext(), s.referenceRequest())
	s.Require().NoError(err)

	s.Empty(result.Symbols)
	s.Equal([]string{".ts: 2 files are not processed: search stopped by timeout"}, result.Warnings)
}

// TestReferenceDeadlineBetweenLocationsOfLoadedFileStopsProcessing checks that a reference search whose deadline
// passes after the symbols of a referencing file were loaded stops before the next location of this file, and counts
// the files of the remaining locations as not processed.
func (s *fileFailureSuite) TestReferenceDeadlineBetweenLocationsOfLoadedFileStopsProcessing() {
	s.referenceLocations = []protocol.Location{s.location("b.ts", 0), s.location("b.ts", 0), s.location("c.ts", 0)}
	service := s.newService(finishAfterDeadline("b.ts", 1), nil)

	result, err := service.FindReferencingSymbols(s.deadlineContext(), s.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"b.ts"}, referencingFiles(result))
	s.Equal([]string{".ts: 2 files are not processed: search stopped by timeout"}, result.Warnings)
}

// TestTimeoutReasonStaysWhenEachNotProcessedFileFailed checks that the warnings keep the timeout reason when each file
// that the search did not process also failed, and that the failure keeps its own warning.
func (s *fileFailureSuite) TestTimeoutReasonStaysWhenEachNotProcessedFileFailed() {
	failures := NewFileFailures(s.workspaceRoot, false)
	ctx := s.expiredContext()

	s.Require().True(failures.StopByTimeout(ctx, []string{"b.php"}))
	s.Require().NoError(failures.RecordSkippedFiles(ctx, []helpers.SkippedFile{
		{RelativePath: "b.php", Err: errors.New("open request document: notification failed")},
	}))
	result := domain.FindReferencingSymbolsResult{
		Symbols:    nil,
		Incomplete: false,
		Warnings:   nil,
	}
	failures.CompleteReferenceResult(&result)

	s.Equal([]string{
		".php: 1 file is not processed: b.php: open request document: notification failed",
		".php: search stopped by timeout",
	}, result.Warnings)
}

// TestTimeoutLineLeavesOutFailedFiles checks that the timeout warning counts only the files that the search did not
// process and that did not fail.
func (s *fileFailureSuite) TestTimeoutLineLeavesOutFailedFiles() {
	failures := NewFileFailures(s.workspaceRoot, false)
	ctx := s.expiredContext()

	s.Require().True(failures.StopByTimeout(ctx, []string{"b.php", "c.php"}))
	s.Require().NoError(failures.RecordSkippedFiles(ctx, []helpers.SkippedFile{
		{RelativePath: "b.php", Err: errors.New("open request document: notification failed")},
	}))
	result := domain.FindReferencingSymbolsResult{
		Symbols:    nil,
		Incomplete: false,
		Warnings:   nil,
	}
	failures.CompleteReferenceResult(&result)

	s.Equal([]string{
		".php: 1 file is not processed: b.php: open request document: notification failed",
		".php: 1 file is not processed: search stopped by timeout",
	}, result.Warnings)
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

// newReferenceWorkflowService builds the service through New with the target file open for the whole reference
// workflow.
func (s *fileFailureSuite) newReferenceWorkflowService(withDocument WithRequestDocumentFunc) *Service {
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

	return service
}

// referenceRequest builds a reference search for the symbol of a.ts that the test language server returns.
func (s *fileFailureSuite) referenceRequest() *domain.FindReferencingSymbolsRequest {
	return &domain.FindReferencingSymbolsRequest{
		Path:          "fixtureSymbol",
		IncludeKinds:  nil,
		ExcludeKinds:  nil,
		WorkspaceRoot: s.workspaceRoot,
		File:          "a.ts",
	}
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
			symbols := []map[string]any{{
				"name":           "fixtureSymbol",
				"kind":           int(protocol.SymbolKindConstant),
				"range":          testRange(0, 0, 0, 20),
				"selectionRange": testRange(0, 13, 0, 14),
			}}
			if s.secondSymbol {
				symbols = append(symbols, map[string]any{
					"name":           "fixtureSymbolTwo",
					"kind":           int(protocol.SymbolKindConstant),
					"range":          testRange(1, 0, 1, 20),
					"selectionRange": testRange(1, 13, 1, 14),
				})
			}

			return symbols, nil
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

// deadlineContext returns a request context whose deadline comes soon enough for a test to wait for it and late
// enough for the in-memory document requests before the waiting one.
func (s *fileFailureSuite) deadlineContext() context.Context {
	ctx, cancel := context.WithTimeout(s.T().Context(), testRequestDeadline)
	s.T().Cleanup(cancel)

	return ctx
}

// expiredContext returns a request context whose deadline has already passed.
func (s *fileFailureSuite) expiredContext() context.Context {
	ctx, cancel := context.WithDeadline(s.T().Context(), time.Now().Add(-time.Second))
	s.T().Cleanup(cancel)

	return ctx
}

// waitForDeadline runs the document requests of other files, lets the first passedCalls document requests of the
// file with the given base name run too, and makes each later document request of this file wait for the end of the
// request context.
func waitForDeadline(baseName string, passedCalls int32) WithRequestDocumentFunc {
	var calls atomic.Int32

	return func(ctx context.Context, _ jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		if filepath.Base(absolutePath) != baseName || calls.Add(1) <= passedCalls {
			return run(ctx)
		}
		<-ctx.Done()

		return ctx.Err()
	}
}

// finishAfterDeadline runs each document request, and makes the document request number fileCall of the file with the
// given base name wait for the end of the request context after it runs.
func finishAfterDeadline(baseName string, fileCall int32) WithRequestDocumentFunc {
	var calls atomic.Int32

	return func(ctx context.Context, _ jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		err := run(ctx)
		if filepath.Base(absolutePath) == baseName && calls.Add(1) == fileCall {
			<-ctx.Done()
		}

		return err
	}
}

// recordDocumentRequests wraps withDocument so that the base name of the file of each document request is appended to
// requestedFiles before the document request runs.
func recordDocumentRequests(withDocument WithRequestDocumentFunc, requestedFiles *[]string) WithRequestDocumentFunc {
	return func(ctx context.Context, conn jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		*requestedFiles = append(*requestedFiles, filepath.Base(absolutePath))

		return withDocument(ctx, conn, absolutePath, run)
	}
}

// symbolFiles returns the file of each symbol of a symbol search result in order.
func symbolFiles(result domain.FindSymbolResult) []string {
	files := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		files = append(files, symbol.File)
	}

	return files
}

// referencingFiles returns the file of each symbol of a reference search result in order.
func referencingFiles(result domain.FindReferencingSymbolsResult) []string {
	files := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		files = append(files, symbol.File)
	}

	return files
}

// testRange builds one LSP range.
func testRange(startLine, startCharacter, endLine, endCharacter uint32) protocol.Range {
	return protocol.Range{
		Start: protocol.Position{Line: startLine, Character: startCharacter},
		End:   protocol.Position{Line: endLine, Character: endCharacter},
	}
}

// testRequestDeadline is the request deadline of the tests that wait for it.
const testRequestDeadline = time.Second

// failingDocument returns err for the file with the given base name and runs the request for other files.
func failingDocument(baseName string, err error) WithRequestDocumentFunc {
	return func(ctx context.Context, _ jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		if filepath.Base(absolutePath) == baseName {
			return err
		}

		return run(ctx)
	}
}
