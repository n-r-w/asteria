package lsptsls

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/domain"
)

// fileFailureSuite checks how the tsls reference workflow and re-export symbols handle files that fail.
type fileFailureSuite struct {
	suite.Suite
}

// TestFileFailures runs the tsls checks of failed files.
func TestFileFailures(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(fileFailureSuite))
}

// TestRunWithReferenceWorkflowFilesSkipsNeighborThatFailsWarmUp proves that a neighbor file whose warm-up
// request fails is left out of the workflow and returned as skipped instead of failing the search.
func (s *fileFailureSuite) TestRunWithReferenceWorkflowFilesSkipsNeighborThatFailsWarmUp() {
	t := s.T()
	workspaceRoot, conn, _ := newReferenceWorkflowTestEnv(t, map[string]string{
		"broken.ts":  "export const broken = 1\n",
		"fixture.ts": "export const fixture = 1\n",
	})
	var warmedPaths []string

	skipped, err := runWithReferenceWorkflowFiles(
		t.Context(),
		conn,
		workspaceRoot,
		[]string{"broken.ts", "fixture.ts"},
		newWithRequestDocument(),
		func(_ context.Context, openedPaths []string, _ []helpers.SkippedFile) error {
			warmedPaths = openedPaths

			return nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, []string{"fixture.ts"}, warmedPaths)
	require.Len(t, skipped, 1)
	require.Equal(t, "broken.ts", skipped[0].RelativePath)
	require.ErrorContains(t, skipped[0].Err, "document symbols are not available")
}

// TestRunWithReferenceWorkflowFilesSkipsNeighborThatFailsLaterWarmUp proves that a neighbor file whose warm-up
// fails after all files are open is also left out and returned as skipped.
func (s *fileFailureSuite) TestRunWithReferenceWorkflowFilesSkipsNeighborThatFailsLaterWarmUp() {
	t := s.T()
	workspaceRoot, conn, _ := newReferenceWorkflowTestEnv(t, map[string]string{
		"late.ts":    "export const late = 1\n",
		"fixture.ts": "export const fixture = 1\n",
	})
	var warmedPaths []string

	skipped, err := runWithReferenceWorkflowFiles(
		t.Context(),
		conn,
		workspaceRoot,
		[]string{"late.ts", "fixture.ts"},
		newWithRequestDocument(),
		func(_ context.Context, openedPaths []string, _ []helpers.SkippedFile) error {
			warmedPaths = openedPaths

			return nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, []string{"fixture.ts"}, warmedPaths)
	require.Len(t, skipped, 1)
	require.Equal(t, "late.ts", skipped[0].RelativePath)
}

// TestRunWithReferenceWorkflowFilesFailsWhenTargetFailsWarmUp proves that a failed warm-up of the target file,
// which comes last, still fails the search.
func (s *fileFailureSuite) TestRunWithReferenceWorkflowFilesFailsWhenTargetFailsWarmUp() {
	t := s.T()
	workspaceRoot, conn, _ := newReferenceWorkflowTestEnv(t, map[string]string{
		"helper.ts": "export const helper = 1\n",
		"broken.ts": "export const broken = 1\n",
	})

	_, err := runWithReferenceWorkflowFiles(
		t.Context(),
		conn,
		workspaceRoot,
		[]string{"helper.ts", "broken.ts"},
		newWithRequestDocument(),
		func(context.Context, []string, []helpers.SkippedFile) error { return nil },
	)
	require.ErrorContains(t, err, "document symbols are not available")
}

// TestRetryIgnoresRowsOfFileSkippedInWarmUp checks that rows of a neighbor skipped in the first warm-up do not
// stop the retry, so the next attempt can return the references of other files.
func (s *fileFailureSuite) TestRetryIgnoresRowsOfFileSkippedInWarmUp() {
	// The second documentSymbol request of b.ts is the warm-up after all files are open.
	env := s.newAdapterEnv(referenceFiles(), referenceAnswers(
		func(name string, call int) bool { return name == "b.ts" && call == 2 },
		[][]testReference{{{"b.ts", 0}}, {{"c.ts", 0}}},
	), nil)

	result, err := env.service.FindReferencingSymbols(s.T().Context(), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"c.ts"}, referencingFiles(result))
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "b.ts")
}

// TestRetryIgnoresRowsOfFileSkippedInRepeatedWarmUp checks that rows of a neighbor that fails the warm-up before
// a repeated lookup do not stop the retry.
func (s *fileFailureSuite) TestRetryIgnoresRowsOfFileSkippedInRepeatedWarmUp() {
	// The third documentSymbol request of b.ts is the warm-up before the second lookup.
	env := s.newAdapterEnv(referenceFiles(), referenceAnswers(
		func(name string, call int) bool { return name == "b.ts" && call == 3 },
		[][]testReference{{}, {{"b.ts", 0}}, {{"c.ts", 0}}},
	), nil)

	result, err := env.service.FindReferencingSymbols(s.T().Context(), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"c.ts"}, referencingFiles(result))
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "b.ts")
}

// TestRetryKeepsFailureOfEarlierAttempt checks that a file that failed in one lookup attempt keeps its warning and
// gets no rows from a later attempt.
func (s *fileFailureSuite) TestRetryKeepsFailureOfEarlierAttempt() {
	// Line 99 is outside b.ts, so b.ts fails in the first attempt after its first row.
	env := s.newAdapterEnv(referenceFiles(), referenceAnswers(
		func(string, int) bool { return false },
		[][]testReference{{{"b.ts", 0}, {"b.ts", 99}}, {{"c.ts", 0}, {"b.ts", 0}}},
	), nil)

	result, err := env.service.FindReferencingSymbols(s.T().Context(), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"c.ts"}, referencingFiles(result))
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "b.ts")
}

// TestRetryStopsOnUsableCrossFileReferences checks that usable references in another file end the retry at once.
func (s *fileFailureSuite) TestRetryStopsOnUsableCrossFileReferences() {
	env := s.newAdapterEnv(referenceFiles(), referenceAnswers(
		func(string, int) bool { return false },
		[][]testReference{{{"c.ts", 0}}, {{"b.ts", 0}}},
	), nil)

	result, err := env.service.FindReferencingSymbols(s.T().Context(), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"c.ts"}, referencingFiles(result))
	s.Empty(result.Warnings)
}

// TestReferencesDeadlineSkipsRetry checks that a lookup stopped by the deadline returns the references found before
// the stop without a retry, although they come only from the target file, and counts the file that it did not process.
func (s *fileFailureSuite) TestReferencesDeadlineSkipsRetry() {
	env := s.newAdapterEnv(referenceFiles(), referenceAnswers(
		noFailedSymbols,
		[][]testReference{{{"a.ts", 0}, {"c.ts", 0}}},
	), nil)
	env.service.Service = s.newSharedService(func(context.Context, string) (jsonrpc2.Conn, error) {
		return env.conn, nil
	}, waitForDeadline(env.service.withRequestDocument, "c.ts"))

	result, err := env.service.FindReferencingSymbols(deadlineContext(s.T()), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"a.ts"}, referencingFiles(result))
	s.Equal([]string{".ts: 1 file is not processed: search stopped by timeout"}, result.Warnings)
}

// TestReferencesDeadlineDuringRetryKeepsFinishedLookup checks that a deadline during the retry keeps the result of
// the last finished lookup and counts the files of the reference workflow as not processed.
func (s *fileFailureSuite) TestReferencesDeadlineDuringRetryKeepsFinishedLookup() {
	// Each lookup finds only the target file, so the retry repeats until the deadline.
	env := s.newAdapterEnv(referenceFiles(), referenceAnswers(
		noFailedSymbols,
		[][]testReference{{{"a.ts", 0}}},
	), nil)

	result, err := env.service.FindReferencingSymbols(deadlineContext(s.T()), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"a.ts"}, referencingFiles(result))
	s.Equal([]string{".ts: 3 files are not processed: search stopped by timeout"}, result.Warnings)
}

// TestReferencesRetryStoppedByDeadlineKeepsFinishedLookup checks that a retry that stops by the deadline before its
// first row keeps the result of the finished lookup and counts the files of the retry that it did not process.
func (s *fileFailureSuite) TestReferencesRetryStoppedByDeadlineKeepsFinishedLookup() {
	env := s.newAdapterEnv(referenceFiles(), referenceAnswers(
		noFailedSymbols,
		[][]testReference{{{"a.ts", 0}}, {{"b.ts", 0}, {"c.ts", 0}}},
	), nil)
	// Each lookup reads the target symbol of a.ts and asks for its references; the deadline passes after the
	// references of the retry arrive.
	env.service.Service = s.newSharedService(func(context.Context, string) (jsonrpc2.Conn, error) {
		return env.conn, nil
	}, finishAfterDeadline(env.service.withRequestDocument, "a.ts", 4))

	result, err := env.service.FindReferencingSymbols(deadlineContext(s.T()), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"a.ts"}, referencingFiles(result))
	s.Equal([]string{".ts: 2 files are not processed: search stopped by timeout"}, result.Warnings)
}

// TestReferencesRetryStoppedByDeadlineAddsItsRows checks that a retry that stops by the deadline after some rows adds
// them to the result of the finished lookup, without a second row for the same container.
func (s *fileFailureSuite) TestReferencesRetryStoppedByDeadlineAddsItsRows() {
	env := s.newAdapterEnv(referenceFiles(), referenceAnswers(
		noFailedSymbols,
		[][]testReference{{{"a.ts", 0}}, {{"a.ts", 0}, {"b.ts", 0}, {"c.ts", 0}}},
	), nil)
	// The retry is the first lookup that reads the symbols of b.ts; the deadline passes after them.
	env.service.Service = s.newSharedService(func(context.Context, string) (jsonrpc2.Conn, error) {
		return env.conn, nil
	}, finishAfterDeadline(env.service.withRequestDocument, "b.ts", 1))

	result, err := env.service.FindReferencingSymbols(deadlineContext(s.T()), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"a.ts", "b.ts"}, referencingFiles(result))
	s.Equal([]string{".ts: 1 file is not processed: search stopped by timeout"}, result.Warnings)
}

// TestReferencesRetryStoppedByDeadlineKeepsRowsOfEachFinishedLookup checks that a retry that stops by the deadline
// adds its rows to the rows of each finished lookup, also when a later finished lookup found no references.
func (s *fileFailureSuite) TestReferencesRetryStoppedByDeadlineKeepsRowsOfEachFinishedLookup() {
	env := s.newAdapterEnv(referenceFiles(), referenceAnswers(
		noFailedSymbols,
		[][]testReference{{{"a.ts", 0}}, {}, {{"b.ts", 0}, {"c.ts", 0}}},
	), nil)
	// The third lookup is the first one that reads the symbols of b.ts; the deadline passes after them.
	env.service.Service = s.newSharedService(func(context.Context, string) (jsonrpc2.Conn, error) {
		return env.conn, nil
	}, finishAfterDeadline(env.service.withRequestDocument, "b.ts", 1))

	result, err := env.service.FindReferencingSymbols(deadlineContext(s.T()), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"a.ts", "b.ts"}, referencingFiles(result))
	s.Equal([]string{".ts: 1 file is not processed: search stopped by timeout"}, result.Warnings)
}

// TestReferencesRetryWithoutDeadlineReturnsLastLookup checks that a retry that finishes before the deadline returns
// the rows of its own lookup only.
func (s *fileFailureSuite) TestReferencesRetryWithoutDeadlineReturnsLastLookup() {
	env := s.newAdapterEnv(referenceFiles(), referenceAnswers(
		noFailedSymbols,
		[][]testReference{{{"a.ts", 0}}, {{"b.ts", 0}}},
	), nil)

	result, err := env.service.FindReferencingSymbols(s.T().Context(), env.referenceRequest())
	s.Require().NoError(err)

	s.Equal([]string{"b.ts"}, referencingFiles(result))
	s.Empty(result.Warnings)
}

// TestFindSymbolLeavesOutNeighborSkippedInAliasDefinition checks that a neighbor file that fails only while the
// alias definition is requested is named in the warning and keeps no symbols.
func (s *fileFailureSuite) TestFindSymbolLeavesOutNeighborSkippedInAliasDefinition() {
	files := aliasFiles()
	files["neighbor.ts"] = "export const shared = 1;\n"
	// The second documentSymbol request of neighbor.ts is the warm-up of the alias definition workflow.
	env := s.newAdapterEnv(files, aliasAnswers(func(name string, call int) bool {
		return name == "neighbor.ts" && call == 2
	}, nil, nil), nil)

	result, err := env.service.FindSymbol(s.T().Context(), env.aliasRequest(false))
	s.Require().NoError(err)

	s.Equal([]string{"alias.ts"}, foundFiles(result))
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "neighbor.ts")
}

// TestReferencesLeaveOutNeighborSkippedInAliasDefinition checks that a neighbor file that fails only while the
// definition of the alias target is requested is named in the warning and keeps no reference rows.
func (s *fileFailureSuite) TestReferencesLeaveOutNeighborSkippedInAliasDefinition() {
	files := aliasFiles()
	files["neighbor.ts"] = "export const shared = 1;\n"
	files["user.ts"] = "export const shared = 1;\n"
	// The third documentSymbol request of neighbor.ts is the warm-up of the alias definition workflow, after the
	// warm-up on open and the warm-up of all open files.
	aliasAnswer := aliasAnswers(func(name string, call int) bool {
		return name == "neighbor.ts" && call == 3
	}, nil, nil)
	attempts := [][]testReference{{{"neighbor.ts", 0}}, {{"user.ts", 0}}}
	env := s.newAdapterEnv(files, func(ctx context.Context, method string, documentPath string, call int) (any, error) {
		if method == protocol.MethodTextDocumentReferences {
			return locations(filepath.Dir(documentPath), attempts[min(call, len(attempts))-1]), nil
		}

		return aliasAnswer(ctx, method, documentPath, call)
	}, nil)
	request := env.referenceRequest()
	request.File = "alias.ts"
	request.Path = "shared"

	result, err := env.service.FindReferencingSymbols(s.T().Context(), request)
	s.Require().NoError(err)

	s.Equal([]string{"user.ts"}, referencingFiles(result))
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "neighbor.ts")
}

// TestFindSymbolNamesSourceFileThatFailsInAliasResolution checks that a source file whose symbols fail while an
// alias that TypeScript resolves to it is built is named in the warning and keeps no symbols, and that the alias
// file is not named.
func (s *fileFailureSuite) TestFindSymbolNamesSourceFileThatFailsInAliasResolution() {
	// The third documentSymbol request of source.ts reads its symbols for the alias, after the symbol search and
	// the warm-up of the alias definition workflow.
	env := s.newAdapterEnv(aliasFiles(), aliasAnswers(func(name string, call int) bool {
		return name == "source.ts" && call == 3
	}, nil, nil), nil)

	result, err := env.service.FindSymbol(s.T().Context(), env.substringAliasRequest())
	s.Require().NoError(err)

	s.Empty(result.Symbols)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "source.ts")
	s.NotContains(result.Warnings[0], "alias.ts")
}

// TestFindSymbolNamesSourceFileThatFailsInExplicitAliasResolution checks the same for an alias that is resolved
// through its module specifier because TypeScript points the definition at the alias itself, and that another
// alias of the same file stays in the result.
func (s *fileFailureSuite) TestFindSymbolNamesSourceFileThatFailsInExplicitAliasResolution() {
	files := aliasFiles()
	files["alias.ts"] += "export { other as sharedOther } from './other';\n"
	files["other.ts"] = "export const other = 1;\n"
	aliasAnswer := aliasAnswers(func(name string, call int) bool {
		return name == "source.ts" && call == 3
	}, nil, nil)
	env := s.newAdapterEnv(files, func(ctx context.Context, method string, documentPath string, call int) (any, error) {
		if method == protocol.MethodTextDocumentDefinition {
			return []protocol.Location{{URI: uri.File(documentPath), Range: testRange(0, 19, 0, 25)}}, nil
		}

		return aliasAnswer(ctx, method, documentPath, call)
	}, nil)

	result, err := env.service.FindSymbol(s.T().Context(), env.substringAliasRequest())
	s.Require().NoError(err)

	s.Require().Equal([]string{"alias.ts"}, foundFiles(result))
	s.Equal("sharedOther", result.Symbols[0].Path)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "source.ts")
	s.NotContains(result.Warnings[0], "alias.ts")
}

// TestFindSymbolNamesUnreadableDefaultExportSource checks that a source file of a default re-export that cannot be
// read is named in the warning instead of the alias file, and that another alias of the alias file stays.
func (s *fileFailureSuite) TestFindSymbolNamesUnreadableDefaultExportSource() {
	files := map[string]string{
		"alias.ts": "export { default as shared } from './source';\n" +
			"export { other as sharedOther } from './other';\n",
		"other.ts": "export const other = 1;\n",
	}
	aliasAnswer := aliasAnswers(noFailedSymbols, nil, nil)
	env := s.newAdapterEnv(files, func(ctx context.Context, method string, documentPath string, call int) (any, error) {
		// The definition points at the alias itself, so each alias is resolved through its module specifier.
		if method == protocol.MethodTextDocumentDefinition {
			return []protocol.Location{{URI: uri.File(documentPath), Range: testRange(0, 20, 0, 26)}}, nil
		}

		return aliasAnswer(ctx, method, documentPath, call)
	}, nil)
	// A directory named source.ts resolves as the module source but cannot be read as a file.
	s.Require().NoError(os.Mkdir(filepath.Join(env.workspaceRoot, "source.ts"), 0o750))

	result, err := env.service.FindSymbol(s.T().Context(), env.substringAliasRequest())
	s.Require().NoError(err)

	s.Require().Equal([]string{"alias.ts"}, foundFiles(result))
	s.Equal("sharedOther", result.Symbols[0].Path)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "source.ts")
	s.NotContains(result.Warnings[0], "alias.ts")
}

// TestFileSearchEndsWhenAliasFileFailsToClose checks that a search of the alias file returns the error of this file
// when it fails to close after the alias definition was requested.
func (s *fileFailureSuite) TestFileSearchEndsWhenAliasFileFailsToClose() {
	env := s.newAdapterEnv(aliasFiles(), aliasAnswers(noFailedSymbols, nil, nil), nil)
	closeErr := errors.New("close request document: notification failed")
	env.service.withRequestDocument = withCloseError(env.service.withRequestDocument, "alias.ts", closeErr)
	request := env.aliasRequest(false)
	request.Scope = "alias.ts"

	_, err := env.service.FindSymbol(s.T().Context(), request)
	s.ErrorIs(err, closeErr)
}

// TestAliasCloseErrorDoesNotHideSessionErrorOfSource checks that a search ends with the session error of the
// source lookup of an alias even when the alias file also fails to close.
func (s *fileFailureSuite) TestAliasCloseErrorDoesNotHideSessionErrorOfSource() {
	sessionErr := errors.New("server stopped")
	// The session stops after the definition request, so only the source lookup of the alias finds no session.
	var stopped atomic.Bool
	env := s.newAdapterEnv(aliasFiles(), aliasAnswers(noFailedSymbols, func() { stopped.Store(true) }, nil), nil)
	env.service.Service = s.newSharedService(func(context.Context, string) (jsonrpc2.Conn, error) {
		if stopped.Load() {
			return nil, sessionErr
		}

		return env.conn, nil
	}, env.service.withRequestDocument)
	env.service.withRequestDocument = withCloseError(
		env.service.withRequestDocument,
		"alias.ts",
		errors.New("close request document: notification failed"),
	)

	_, err := env.service.FindSymbol(s.T().Context(), env.aliasRequest(false))
	s.ErrorIs(err, sessionErr)
}

// TestOverviewEndsWhenRequestedFileFailsToClose checks that an overview of the alias file returns the error of this
// file when it fails to close after the alias definition was requested.
func (s *fileFailureSuite) TestOverviewEndsWhenRequestedFileFailsToClose() {
	env := s.newAdapterEnv(aliasFiles(), aliasAnswers(noFailedSymbols, nil, nil), nil)
	closeErr := errors.New("close request document: notification failed")
	env.service.withRequestDocument = withCloseError(env.service.withRequestDocument, "alias.ts", closeErr)

	result, err := env.service.GetSymbolsOverview(s.T().Context(), env.overviewRequest())
	s.Require().ErrorIs(err, closeErr)
	s.Empty(result.Symbols)
}

// TestOverviewIgnoresNeighborSkippedInAliasDefinition checks that an overview of the alias file keeps the alias when
// only a neighbor file fails while the alias definition is requested.
func (s *fileFailureSuite) TestOverviewIgnoresNeighborSkippedInAliasDefinition() {
	files := aliasFiles()
	files["neighbor.ts"] = "export const shared = 1;\n"
	// The first documentSymbol request of neighbor.ts is the warm-up of the alias definition workflow.
	env := s.newAdapterEnv(files, aliasAnswers(func(name string, call int) bool {
		return name == "neighbor.ts" && call == 1
	}, nil, nil), nil)

	result, err := env.service.GetSymbolsOverview(s.T().Context(), env.overviewRequest())
	s.Require().NoError(err)

	s.Require().Len(result.Symbols, 1)
	s.Equal("shared", result.Symbols[0].Path)
}

// TestFileSearchIgnoresNeighborSkippedInAliasDefinition checks that a search of the alias file returns the alias
// without a warning when a neighbor outside the scope fails while the alias definition is requested.
func (s *fileFailureSuite) TestFileSearchIgnoresNeighborSkippedInAliasDefinition() {
	files := aliasFiles()
	files["neighbor.ts"] = "export const shared = 1;\n"
	// The first documentSymbol request of neighbor.ts is the warm-up of the alias definition workflow.
	env := s.newAdapterEnv(files, aliasAnswers(func(name string, call int) bool {
		return name == "neighbor.ts" && call == 1
	}, nil, nil), nil)
	request := env.aliasRequest(false)
	request.Scope = "alias.ts"

	result, err := env.service.FindSymbol(s.T().Context(), request)
	s.Require().NoError(err)

	s.Equal([]string{"alias.ts"}, foundFiles(result))
	s.Empty(result.Warnings)
}

// TestOrdinaryHoverErrorKeepsAlias checks that an ordinary hover error leaves the alias in the result without info.
func (s *fileFailureSuite) TestOrdinaryHoverErrorKeepsAlias() {
	env := s.newAdapterEnv(aliasFiles(), aliasAnswers(noFailedSymbols, nil, func(context.Context) (any, error) {
		return nil, errors.New("hover is not available")
	}), nil)

	result, err := env.service.FindSymbol(s.T().Context(), env.aliasRequest(true))
	s.Require().NoError(err)

	s.Require().Equal([]string{"alias.ts"}, foundFiles(result))
	s.Empty(result.Symbols[0].Info)
	s.Empty(result.Warnings)
}

// TestHoverAfterEndedRequestContextEndsRequest checks that a hover error after the request context ended ends the
// search.
func (s *fileFailureSuite) TestHoverAfterEndedRequestContextEndsRequest() {
	ctx, cancel := context.WithCancel(s.T().Context())
	env := s.newAdapterEnv(aliasFiles(), aliasAnswers(noFailedSymbols, nil, func(context.Context) (any, error) {
		cancel()

		return nil, errors.New("hover is canceled")
	}), nil)

	_, err := env.service.FindSymbol(ctx, env.aliasRequest(true))
	// The error is either the ended context or the hover error, depending on which one the client sees first.
	s.Error(err)
}

// TestHoverWithoutSessionEndsRequest checks that a hover request without a language server session ends the search.
func (s *fileFailureSuite) TestHoverWithoutSessionEndsRequest() {
	sessionErr := errors.New("server stopped")
	// The session stops after the definition request, so only the hover request of the alias finds no session.
	var stopped atomic.Bool
	env := s.newAdapterEnv(aliasFiles(), aliasAnswers(noFailedSymbols, func() { stopped.Store(true) }, nil),
		func() error {
			if stopped.Load() {
				return sessionErr
			}

			return nil
		})

	_, err := env.service.FindSymbol(s.T().Context(), env.aliasRequest(true))
	s.ErrorIs(err, sessionErr)
}

// TestFindSymbolSkipsReexportsAfterDeadline checks that a workspace search stopped by the deadline returns the symbols
// of the files processed before the stop without scanning re-exports, and counts the file that was not searched.
func (s *fileFailureSuite) TestFindSymbolSkipsReexportsAfterDeadline() {
	files := aliasFiles()
	files["neighbor.ts"] = "export const shared = 1;\n"
	env := s.newAdapterEnv(files, aliasAnswers(noFailedSymbols, nil, nil), nil)
	env.service.Service = s.newSharedService(func(context.Context, string) (jsonrpc2.Conn, error) {
		return env.conn, nil
	}, waitForDeadline(env.service.withRequestDocument, "source.ts"))

	result, err := env.service.FindSymbol(deadlineContext(s.T()), env.aliasRequest(false))
	s.Require().NoError(err)

	s.Equal([]string{"neighbor.ts"}, foundFiles(result))
	s.Equal([]string{".ts: 1 file is not searched: search stopped by timeout"}, result.Warnings)
}

// TestFindSymbolKeepsSymbolsWhenDeadlineStopsReexportScan checks that a deadline during the re-export scan keeps the
// symbols of the shared search and counts the files whose re-exports were not scanned.
func (s *fileFailureSuite) TestFindSymbolKeepsSymbolsWhenDeadlineStopsReexportScan() {
	files := aliasFiles()
	files["neighbor.ts"] = "export const shared = 1;\n"
	env := s.newAdapterEnv(files, aliasAnswers(noFailedSymbols, nil, nil), nil)
	// The shared search keeps its own document callback, so only the requests of the re-export scan wait.
	env.service.withRequestDocument = waitForDeadline(env.service.withRequestDocument, "")

	result, err := env.service.FindSymbol(deadlineContext(s.T()), env.aliasRequest(false))
	s.Require().NoError(err)

	s.Equal([]string{"neighbor.ts"}, foundFiles(result))
	s.Equal([]string{".ts: 3 files are not searched: search stopped by timeout"}, result.Warnings)
}

// TestFindSymbolSkipsReexportScanWhenDeadlinePassesAfterSearch checks that a directory search whose last file
// finishes after the deadline returns the symbols of the finished search without the re-export scan, also when the
// scope can no longer be listed, and counts the files of the scope as not searched.
func (s *fileFailureSuite) TestFindSymbolSkipsReexportScanWhenDeadlinePassesAfterSearch() {
	env := s.newAdapterEnv(
		map[string]string{"scope/neighbor.ts": "export const shared = 1;\n"},
		aliasAnswers(noFailedSymbols, nil, nil),
		nil,
	)
	scopeDir := filepath.Join(env.workspaceRoot, "scope")
	withRequestDocument := env.service.withRequestDocument
	env.service.Service = s.newSharedService(func(context.Context, string) (jsonrpc2.Conn, error) {
		return env.conn, nil
	}, func(ctx context.Context, conn jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		err := withRequestDocument(ctx, conn, absolutePath, run)
		s.Require().NoError(os.RemoveAll(scopeDir))
		<-ctx.Done()

		return err
	})
	request := env.aliasRequest(false)
	request.Scope = "scope"

	result, err := env.service.FindSymbol(deadlineContext(s.T()), request)
	s.Require().NoError(err)

	s.Equal([]string{"scope/neighbor.ts"}, foundFiles(result))
	s.Equal([]string{".ts: 1 file is not searched: search stopped by timeout"}, result.Warnings)
}

// TestFindSymbolStopsReexportScanBeforeLaterFile checks that a deadline that passes while the re-export scan processes
// one file keeps the aliases of this file, starts no later file, and counts the later files as not searched.
func (s *fileFailureSuite) TestFindSymbolStopsReexportScanBeforeLaterFile() {
	files := aliasFiles()
	files["neighbor.ts"] = "export const shared = 1;\n"
	env := s.newAdapterEnv(files, aliasAnswers(noFailedSymbols, nil, nil), nil)
	// The first request of source.ts belongs to the shared search, and the second one resolves the alias of alias.ts,
	// the first file of the re-export scan.
	env.service.Service = s.newSharedService(func(context.Context, string) (jsonrpc2.Conn, error) {
		return env.conn, nil
	}, finishAfterDeadline(env.service.withRequestDocument, "source.ts", 2))

	result, err := env.service.FindSymbol(deadlineContext(s.T()), env.aliasRequest(false))
	s.Require().NoError(err)

	s.Equal([]string{"alias.ts", "neighbor.ts"}, foundFiles(result))
	s.Equal([]string{".ts: 2 files are not searched: search stopped by timeout"}, result.Warnings)
}

// TestFindSymbolStopsAliasResolutionAfterDeadline checks that a deadline that passes when one alias of a file is
// resolved keeps this alias, starts no resolution of the next alias, and counts the file and the later files as not
// searched.
func (s *fileFailureSuite) TestFindSymbolStopsAliasResolutionAfterDeadline() {
	env := s.newAdapterEnv(map[string]string{
		"alias.ts":  "export { source as shared, source as sharedTwo } from './source';\n",
		"source.ts": "export const source = 1;\n",
	}, aliasAnswers(noFailedSymbols, nil, nil), nil)
	var lateFiles []string
	// The first document request of source.ts belongs to the shared search, and the second one resolves the first
	// alias.
	env.service.Service = s.newSharedService(func(context.Context, string) (jsonrpc2.Conn, error) {
		return env.conn, nil
	}, recordLateDocumentRequests(
		finishAfterDeadline(env.service.withRequestDocument, "source.ts", 2),
		&lateFiles,
	))
	env.service.withRequestDocument = recordLateDocumentRequests(env.service.withRequestDocument, &lateFiles)
	request := env.aliasRequest(false)
	request.SubstringMatching = true

	result, err := env.service.FindSymbol(deadlineContext(s.T()), request)
	s.Require().NoError(err)

	paths := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		paths = append(paths, symbol.Path)
	}
	s.Equal([]string{"shared"}, paths)
	s.Equal([]string{".ts: 2 files are not searched: search stopped by timeout"}, result.Warnings)
	s.Empty(lateFiles, "the second alias must not be resolved")
}

// testAnswer answers one language server request of a test about the file at documentPath; call counts the
// requests of this method for this file from 1.
type testAnswer func(ctx context.Context, method string, documentPath string, call int) (any, error)

// adapterEnv holds a tsls service whose language server is an in-memory peer that answers from a test script.
type adapterEnv struct {
	service       *Service
	conn          jsonrpc2.Conn
	workspaceRoot string
}

// referenceRequest builds the reference search for the symbol that the test language server returns in a.ts.
func (e *adapterEnv) referenceRequest() *domain.FindReferencingSymbolsRequest {
	return &domain.FindReferencingSymbolsRequest{
		Path:          "fixtureSymbol",
		IncludeKinds:  nil,
		ExcludeKinds:  nil,
		WorkspaceRoot: e.workspaceRoot,
		File:          "a.ts",
	}
}

// aliasRequest builds the workspace symbol search for the alias "shared" that alias.ts re-exports.
func (e *adapterEnv) aliasRequest(includeInfo bool) *domain.FindSymbolRequest {
	return &domain.FindSymbolRequest{
		Path:              "shared",
		IncludeKinds:      nil,
		ExcludeKinds:      nil,
		Depth:             0,
		IncludeBody:       false,
		IncludeInfo:       includeInfo,
		SubstringMatching: false,
		WorkspaceRoot:     e.workspaceRoot,
		Scope:             "",
	}
}

// noFailedSymbols lets each documentSymbol request of the test language server succeed.
func noFailedSymbols(string, int) bool {
	return false
}

// overviewRequest builds the overview of alias.ts.
func (e *adapterEnv) overviewRequest() *domain.GetSymbolsOverviewRequest {
	return &domain.GetSymbolsOverviewRequest{
		Depth:         0,
		WorkspaceRoot: e.workspaceRoot,
		File:          "alias.ts",
	}
}

// substringAliasRequest builds the workspace symbol search for each symbol whose name contains "s", which matches
// the alias "shared" and its source "source".
func (e *adapterEnv) substringAliasRequest() *domain.FindSymbolRequest {
	request := e.aliasRequest(false)
	request.Path = "s"
	request.SubstringMatching = true

	return request
}

// aliasFiles returns alias.ts, which re-exports the symbol of source.ts as "shared", and source.ts.
func aliasFiles() map[string]string {
	return map[string]string{
		"alias.ts":  "export { source as shared } from './source';\n",
		"source.ts": "export const source = 1;\n",
	}
}

// aliasAnswers answers documentSymbol with the symbol "shared" for neighbor.ts and user.ts, "source" for source.ts,
// "other" for other.ts, and no symbols for other files unless failSymbols selects the request; definition with the
// symbol of source.ts after onDefinition runs; and hover through hover.
func aliasAnswers(
	failSymbols func(fileName string, call int) bool,
	onDefinition func(),
	hover func(ctx context.Context) (any, error),
) testAnswer {
	return func(ctx context.Context, method string, documentPath string, call int) (any, error) {
		fileName := filepath.Base(documentPath)
		switch method {
		case protocol.MethodTextDocumentDocumentSymbol:
			if failSymbols(fileName, call) {
				return nil, errors.New("document symbols are not available")
			}
			switch fileName {
			case "neighbor.ts", "user.ts":
				return []protocol.DocumentSymbol{testSymbol("shared", 0)}, nil
			case "source.ts":
				return []protocol.DocumentSymbol{testSymbol("source", 0)}, nil
			case "other.ts":
				return []protocol.DocumentSymbol{testSymbol("other", 0)}, nil
			default:
				return []protocol.DocumentSymbol{}, nil
			}
		case protocol.MethodTextDocumentDefinition:
			if onDefinition != nil {
				onDefinition()
			}

			return []protocol.Location{{
				URI:   uri.File(filepath.Join(filepath.Dir(documentPath), "source.ts")),
				Range: testRange(0, 13, 0, 19),
			}}, nil
		case protocol.MethodTextDocumentHover:
			return hover(ctx)
		default:
			return nil, errors.New("method is not supported by the test language server")
		}
	}
}

// testReference is one reference location that the test language server returns.
type testReference struct {
	file string
	line uint32
}

// referenceFiles returns the target a.ts and its neighbors b.ts and c.ts.
func referenceFiles() map[string]string {
	return map[string]string{
		"a.ts": "export const x = 1;\n",
		"b.ts": "export const x = 1;\n",
		"c.ts": "export const x = 1;\n",
	}
}

// referenceAnswers answers documentSymbol with one symbol unless failSymbols selects the request, and the n-th
// references request with the locations of attempts[n]; later requests get the last answer.
func referenceAnswers(
	failSymbols func(fileName string, call int) bool,
	attempts [][]testReference,
) testAnswer {
	var (
		mu             sync.Mutex
		referenceCalls int
	)

	return func(_ context.Context, method string, documentPath string, call int) (any, error) {
		switch method {
		case protocol.MethodTextDocumentDocumentSymbol:
			if failSymbols(filepath.Base(documentPath), call) {
				return nil, errors.New("document symbols are not available")
			}

			return []protocol.DocumentSymbol{testSymbol("fixtureSymbol", 0)}, nil
		case protocol.MethodTextDocumentReferences:
			mu.Lock()
			referenceCalls++
			answer := attempts[min(referenceCalls, len(attempts))-1]
			mu.Unlock()

			return locations(filepath.Dir(documentPath), answer), nil
		default:
			return nil, errors.New("method is not supported by the test language server")
		}
	}
}

// newAdapterEnv writes files into a new workspace and creates a tsls service whose language server answers through
// answer. connErr, when set, gives the error of each connection request of the adapter outside the shared workflow.
func (s *fileFailureSuite) newAdapterEnv(files map[string]string, answer testAnswer, connErr func() error) *adapterEnv {
	t := s.T()
	workspaceRoot := t.TempDir()
	writeWorkspaceFiles(t, workspaceRoot, files)
	conn := s.newAnswerConn(answer)

	rt, err := runtimelsp.New(&runtimelsp.RuntimeConfig{
		Command:                 tslsServerName,
		Args:                    nil,
		ServerName:              tslsServerName,
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

	withRequestDocument := helpers.WithRequestDocument(languageIDForExtension)
	std := s.newSharedService(func(context.Context, string) (jsonrpc2.Conn, error) {
		return conn, nil
	}, withRequestDocument)

	return &adapterEnv{
		service: &Service{
			Service: std,
			rt:      rt,
			ensureConn: func(context.Context, string) (jsonrpc2.Conn, error) {
				if connErr != nil {
					if err := connErr(); err != nil {
						return nil, err
					}
				}

				return conn, nil
			},
			withRequestDocument: withRequestDocument,
		},
		conn:          conn,
		workspaceRoot: workspaceRoot,
	}
}

// newSharedService creates the shared standard-LSP service of the tsls adapter with the given connection source.
func (s *fileFailureSuite) newSharedService(
	ensureConn stdlsp.EnsureConnFunc,
	withRequestDocument stdlsp.WithRequestDocumentFunc,
) *stdlsp.Service {
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

	return std
}

// withCloseError wraps withRequestDocument so that each request with the file fileName also returns closeErr, the
// way WithRequestDocument joins a failed didClose with the result of the request.
func withCloseError(
	withRequestDocument stdlsp.WithRequestDocumentFunc,
	fileName string,
	closeErr error,
) stdlsp.WithRequestDocumentFunc {
	return func(ctx context.Context, conn jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		err := withRequestDocument(ctx, conn, absolutePath, run)
		if filepath.Base(absolutePath) == fileName {
			return errors.Join(err, closeErr)
		}

		return err
	}
}

// waitForDeadline wraps withRequestDocument so that each document request with the file fileName, or with any file
// when fileName is empty, waits for the end of the request context and returns its error.
func waitForDeadline(
	withRequestDocument stdlsp.WithRequestDocumentFunc,
	fileName string,
) stdlsp.WithRequestDocumentFunc {
	return func(ctx context.Context, conn jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		if fileName != "" && filepath.Base(absolutePath) != fileName {
			return withRequestDocument(ctx, conn, absolutePath, run)
		}
		<-ctx.Done()

		return ctx.Err()
	}
}

// finishAfterDeadline wraps withRequestDocument so that each document request runs, and the document request number
// fileCall with the file fileName waits for the end of the request context after it runs.
func finishAfterDeadline(
	withRequestDocument stdlsp.WithRequestDocumentFunc,
	fileName string,
	fileCall int32,
) stdlsp.WithRequestDocumentFunc {
	var fileCalls atomic.Int32

	return func(ctx context.Context, conn jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		err := withRequestDocument(ctx, conn, absolutePath, run)
		if filepath.Base(absolutePath) == fileName && fileCalls.Add(1) == fileCall {
			<-ctx.Done()
		}

		return err
	}
}

// recordLateDocumentRequests wraps withRequestDocument so that the base name of the file of each document request
// that starts after the deadline of its request context is appended to lateFiles.
func recordLateDocumentRequests(
	withRequestDocument stdlsp.WithRequestDocumentFunc,
	lateFiles *[]string,
) stdlsp.WithRequestDocumentFunc {
	return func(ctx context.Context, conn jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		if domain.IsStoppedByTimeout(ctx) {
			*lateFiles = append(*lateFiles, filepath.Base(absolutePath))
		}

		return withRequestDocument(ctx, conn, absolutePath, run)
	}
}

// deadlineContext returns a request context of the test t whose deadline comes soon enough for the test to wait for
// it and late enough for the in-memory document requests before the waiting one.
func deadlineContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	return ctx
}

// newAnswerConn builds an in-memory language server that accepts document notifications and answers each request
// through answer.
func (s *fileFailureSuite) newAnswerConn(answer testAnswer) jsonrpc2.Conn {
	t := s.T()
	var (
		mu    sync.Mutex
		calls = make(map[string]int)
	)
	serverSide, clientSide := net.Pipe()
	serverConn := jsonrpc2.NewConn(jsonrpc2.NewStream(serverSide))
	clientConn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	serverConn.Go(t.Context(), func(ctx context.Context, req *jsonrpc2.Request) (any, error) {
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

			return answer(ctx, method, documentPath, call)
		}
	})
	clientConn.Go(t.Context(), jsonrpc2.MethodNotFoundHandler)
	t.Cleanup(func() {
		s.NoError(clientConn.Close())
		s.NoError(serverConn.Close())
		waitForConnDone(t, "clientConn", clientConn.Done())
		waitForConnDone(t, "serverConn", serverConn.Done())
	})

	return clientConn
}

// locations converts test references to files of dir into reference locations at the symbol of the test
// language server.
func locations(dir string, references []testReference) []protocol.Location {
	result := make([]protocol.Location, 0, len(references))
	for _, reference := range references {
		result = append(result, protocol.Location{
			URI:   uri.File(filepath.Join(dir, reference.file)),
			Range: testRange(reference.line, 13, reference.line, 14),
		})
	}

	return result
}

// testSymbol builds one constant symbol whose name starts at column 13 of the given line.
func testSymbol(name string, line uint32) protocol.DocumentSymbol {
	return protocol.DocumentSymbol{
		Name:           name,
		Detail:         nil,
		Kind:           protocol.SymbolKindConstant,
		Tags:           nil,
		Deprecated:     nil,
		Range:          testRange(line, 0, line, 20),
		SelectionRange: testRange(line, 13, line, 14),
		Children:       nil,
	}
}

// foundFiles returns the file of each symbol of a symbol search result in order.
func foundFiles(result domain.FindSymbolResult) []string {
	files := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		files = append(files, symbol.File)
	}

	return files
}

// referencingFiles returns the file of each row of a reference search result in order.
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
