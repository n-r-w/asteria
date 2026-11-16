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

// inMemorySearchSuite checks phpactor searches against an in-memory language server.
type inMemorySearchSuite struct {
	suite.Suite
}

// TestInMemorySearch runs the checks of phpactor searches against an in-memory language server.
func TestInMemorySearch(t *testing.T) {
	t.Parallel()

	suite.Run(t, new(inMemorySearchSuite))
}

// TestReferenceSearchWaitsForEndOfIndexing checks that a reference search before phpactor ends the indexing progress,
// whose token the session announced, sends no request to the language server and ends with the deadline error, also
// after a progress that the session did not announce begins first and ends, and that a search after the end of the
// indexing progress returns references that are not marked incomplete.
func (s *inMemorySearchSuite) TestReferenceSearchWaitsForEndOfIndexing() {
	var requests atomic.Int32
	service, request, server := s.newServiceWithServer(
		fixtureFiles(),
		func(method string, documentPath string, call int) (any, error) {
			requests.Add(1)

			return fixtureAnswer(nil)(method, documentPath, call)
		},
		nil,
		nil,
	)
	s.notifyProgress(server, otherProgressToken, progressKindBegin)
	s.createProgress(server, indexingToken)
	s.notifyProgress(server, indexingToken, progressKindBegin)
	s.notifyProgress(server, otherProgressToken, progressKindEnd)

	_, err := service.FindReferencingSymbols(deadlineContext(s.T()), request)
	s.Require().ErrorIs(err, context.DeadlineExceeded)
	s.Zero(requests.Load())

	s.notifyProgress(server, indexingToken, progressKindEnd)
	result, err := service.FindReferencingSymbols(deadlineContext(s.T()), request)
	s.Require().NoError(err)
	s.Equal([]string{"Fixture/read"}, referencingPaths(result))
	s.False(result.Incomplete)
}

// TestReferenceSearchWithoutIndexingProgressIsIncomplete checks that each reference search of a session in which no
// indexing progress begins within the start timeout returns references marked incomplete without warnings, also after
// a progress that the session did not announce begins and ends, and that a search after an indexing progress begins
// later waits for its end again.
func (s *inMemorySearchSuite) TestReferenceSearchWithoutIndexingProgressIsIncomplete() {
	service, request, server := s.newServiceWithServer(fixtureFiles(), fixtureAnswer(nil), nil, nil)
	service.indexingStartTimeout = shortIndexingStartTimeout

	for search := range 2 {
		result, err := service.FindReferencingSymbols(deadlineContext(s.T()), request)
		s.Require().NoError(err)
		s.Equal([]string{"Fixture/read"}, referencingPaths(result))
		s.True(result.Incomplete)
		s.Empty(result.Warnings)
		// phpactor reports each reference search through a progress that it does not announce.
		if search == 0 {
			s.notifyProgress(server, otherProgressToken, progressKindBegin)
			s.notifyProgress(server, otherProgressToken, progressKindEnd)
		}
	}

	s.createProgress(server, indexingToken)
	s.notifyProgress(server, indexingToken, progressKindBegin)
	_, err := service.FindReferencingSymbols(deadlineContext(s.T()), request)
	s.Require().ErrorIs(err, context.DeadlineExceeded)

	s.notifyProgress(server, indexingToken, progressKindEnd)
	result, err := service.FindReferencingSymbols(deadlineContext(s.T()), request)
	s.Require().NoError(err)
	s.False(result.Incomplete)
}

// TestNewSessionWaitsForItsOwnIndexing checks that a reference search of a new session waits for the end of indexing
// of this session, even when an earlier session of the same workspace reported it.
func (s *inMemorySearchSuite) TestNewSessionWaitsForItsOwnIndexing() {
	service, request := s.newService(fixtureFiles(), fixtureAnswer(nil), nil, nil)
	_, err := service.FindReferencingSymbols(deadlineContext(s.T()), request)
	s.Require().NoError(err)

	// The runtime calls this hook before the initialize request of each new session.
	s.Require().NoError(service.patchInitializeParams(request.WorkspaceRoot, &protocol.InitializeParams{}))

	_, err = service.FindReferencingSymbols(deadlineContext(s.T()), request)
	s.Require().ErrorIs(err, context.DeadlineExceeded)
}

// TestOtherErrorKeepsPrimaryResult checks that an ordinary error of the property reference fallback leaves the
// references of the language server in the result.
func (s *inMemorySearchSuite) TestOtherErrorKeepsPrimaryResult() {
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
func (s *inMemorySearchSuite) TestEndedRequestContextEndsRequest() {
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
func (s *inMemorySearchSuite) TestUnavailableSessionEndsRequest() {
	sessionErr := errors.New("server stopped")
	service, request := s.newService(fixtureFiles(), fixtureAnswer(nil), sessionErr, nil)

	_, err := service.FindReferencingSymbols(s.T().Context(), request)
	s.ErrorIs(err, sessionErr)
}

// TestTargetFileUsageFailureKeepsOtherFallbackReferences checks that a property usage in the target file that the
// fallback cannot process names the target file in the warning, while the usages of other files stay.
func (s *inMemorySearchSuite) TestTargetFileUsageFailureKeepsOtherFallbackReferences() {
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

// TestDeadlineSkipsConstantFallback checks that a workspace search stopped by the deadline returns the symbols of the
// files processed before the stop without the constant fallback, and counts the files that were not searched.
func (s *inMemorySearchSuite) TestDeadlineSkipsConstantFallback() {
	service, request := s.newConstantSearch(
		func(withRequestDocument stdlsp.WithRequestDocumentFunc) stdlsp.WithRequestDocumentFunc {
			return waitForDeadline(withRequestDocument, "b.php")
		},
	)

	result, err := service.FindSymbol(deadlineContext(s.T()), request)
	s.Require().NoError(err)

	s.Equal([]string{"a.php"}, foundFiles(result))
	s.Equal([]string{".php: 2 files are not searched: search stopped by timeout"}, result.Warnings)
}

// TestDeadlineAfterSearchSkipsConstantFallback checks that a workspace search whose last file finishes after the
// deadline returns the symbols of the finished search without the constant fallback, and counts the PHP files of the
// scope as not searched because their constants are left out.
func (s *inMemorySearchSuite) TestDeadlineAfterSearchSkipsConstantFallback() {
	service, request := s.newConstantSearch(
		func(withRequestDocument stdlsp.WithRequestDocumentFunc) stdlsp.WithRequestDocumentFunc {
			return finishAfterDeadline(withRequestDocument, "c.php", 1, nil)
		},
	)

	result, err := service.FindSymbol(deadlineContext(s.T()), request)
	s.Require().NoError(err)

	s.Equal([]string{"a.php", "b.php", "c.php"}, foundFiles(result))
	s.Equal([]string{".php: 3 files are not searched: search stopped by timeout"}, result.Warnings)
}

// TestDeadlineSkipsPropertyFallback checks that a reference search stopped by the deadline returns without the
// property reference fallback and counts the file that it did not process.
func (s *inMemorySearchSuite) TestDeadlineSkipsPropertyFallback() {
	files := fixtureFiles()
	files["b.php"] = usageFixture("B")
	service, request := s.newService(files, usageAnswer(), nil, nil)
	s.wrapSharedService(
		service,
		func(withRequestDocument stdlsp.WithRequestDocumentFunc) stdlsp.WithRequestDocumentFunc {
			return waitForDeadline(withRequestDocument, "b.php")
		},
	)
	var fallbackRuns atomic.Int32
	service.runPropertyReferences = func(context.Context, string, string, string, string) ([]phpactorReferenceRow, error) {
		fallbackRuns.Add(1)

		return nil, nil
	}

	result, err := service.FindReferencingSymbols(deadlineContext(s.T()), request)
	s.Require().NoError(err)

	s.Empty(result.Symbols)
	s.Equal([]string{".php: 1 file is not processed: search stopped by timeout"}, result.Warnings)
	s.Zero(fallbackRuns.Load())
}

// TestConstantScanAfterDeadlineAddsNoConstants checks that a constant fallback of a workspace search that starts after
// the deadline adds no constants and counts the files of the scan as not searched.
func (s *inMemorySearchSuite) TestConstantScanAfterDeadlineAddsNoConstants() {
	_, request := s.newConstantSearch(
		func(withRequestDocument stdlsp.WithRequestDocumentFunc) stdlsp.WithRequestDocumentFunc {
			return withRequestDocument
		},
	)
	ctx, cancel := context.WithDeadline(s.T().Context(), time.Now().Add(-time.Second))
	defer cancel()
	failures := stdlsp.NewFileFailures(request.WorkspaceRoot, false)
	result := domain.FindSymbolResult{Symbols: nil, Warnings: nil}

	result, err := addPHPConstants(ctx, request.WorkspaceRoot, request, result, failures)
	s.Require().NoError(err)
	failures.CompleteSymbolResult(&result)

	s.Empty(result.Symbols)
	s.Equal([]string{".php: 3 files are not searched: search stopped by timeout"}, result.Warnings)
}

// TestDeadlineAfterReferenceSearchSkipsPropertyFallback checks that a reference search that finishes after the
// deadline returns its references without the property reference fallback, and counts the target file as not
// processed.
func (s *inMemorySearchSuite) TestDeadlineAfterReferenceSearchSkipsPropertyFallback() {
	files := fixtureFiles()
	files["b.php"] = usageFixture("B")
	service, request := s.newService(files, usageAnswer(), nil, nil)
	var requests atomic.Int32
	// The symbols of b.php, the only referencing file, load last; the deadline passes after them.
	s.wrapSharedService(
		service,
		func(withRequestDocument stdlsp.WithRequestDocumentFunc) stdlsp.WithRequestDocumentFunc {
			return finishAfterDeadline(withRequestDocument, "b.php", 1, &requests)
		},
	)

	result, err := service.FindReferencingSymbols(deadlineContext(s.T()), request)
	s.Require().NoError(err)

	s.Require().Len(result.Symbols, 1)
	s.Equal("B/useIt", result.Symbols[0].Path)
	s.Equal([]string{".php: 1 file is not processed: search stopped by timeout"}, result.Warnings)
	// The target symbol, the references, and the symbols of b.php take one request each.
	s.Equal(int32(3), requests.Load(), "the fallback must not request documents")
}

// TestPropertyFallbackKeepsRowsBeforeFileStoppedByDeadline checks that a property reference fallback whose file
// reaches the deadline keeps the fallback rows of the files before it and counts the files of the rest of its rows
// as not processed.
func (s *inMemorySearchSuite) TestPropertyFallbackKeepsRowsBeforeFileStoppedByDeadline() {
	files := fixtureFiles()
	files["b.php"] = usageFixture("B")
	files["c.php"] = usageFixture("C")
	service, request := s.newService(files, propertyAnswer(map[string][]protocol.DocumentSymbol{
		"b.php": usageSymbols("B"),
		"c.php": usageSymbols("C"),
	}), nil, []phpactorReferenceRow{{File: "b.php", Line: 2}, {File: "c.php", Line: 2}})
	s.wrapSharedService(
		service,
		func(withRequestDocument stdlsp.WithRequestDocumentFunc) stdlsp.WithRequestDocumentFunc {
			return waitForDeadline(withRequestDocument, "c.php")
		},
	)

	result, err := service.FindReferencingSymbols(deadlineContext(s.T()), request)
	s.Require().NoError(err)

	s.Equal([]string{"B/useIt", "Fixture/read"}, referencingPaths(result))
	s.Equal([]string{".php: 1 file is not processed: search stopped by timeout"}, result.Warnings)
}

// TestPropertyFallbackStopsBeforeNextRowOfLoadedFile checks that a property reference fallback whose deadline passes
// when the symbols of a file have loaded keeps the row that needed them, skips the next row of the same file, and
// counts the file as not processed.
func (s *inMemorySearchSuite) TestPropertyFallbackStopsBeforeNextRowOfLoadedFile() {
	files := fixtureFiles()
	files["b.php"] = "<?php\n" +
		"class B {\n" +
		"    public function first(Fixture $fixture) { return $fixture->value; }\n" +
		"    public function second(Fixture $fixture) { return $fixture->value; }\n" +
		"}\n"
	service, request := s.newService(files, propertyAnswer(map[string][]protocol.DocumentSymbol{
		"b.php": {{
			Name:           "B",
			Detail:         nil,
			Kind:           protocol.SymbolKindClass,
			Tags:           nil,
			Deprecated:     nil,
			Range:          fixtureRange(1, 0, 4, 1),
			SelectionRange: fixtureRange(1, 6, 1, 7),
			Children: []protocol.DocumentSymbol{
				methodSymbol("first", 2),
				methodSymbol("second", 3),
			},
		}},
	}), nil, []phpactorReferenceRow{{File: "b.php", Line: 2}, {File: "b.php", Line: 3}})
	s.wrapSharedService(
		service,
		func(withRequestDocument stdlsp.WithRequestDocumentFunc) stdlsp.WithRequestDocumentFunc {
			return finishAfterDeadline(withRequestDocument, "b.php", 1, nil)
		},
	)

	result, err := service.FindReferencingSymbols(deadlineContext(s.T()), request)
	s.Require().NoError(err)

	s.Equal([]string{"B/first", "Fixture/read"}, referencingPaths(result))
	s.Equal([]string{".php: 1 file is not processed: search stopped by timeout"}, result.Warnings)
}

// TestPropertyFallbackErrorAfterDeadlineKeepsSearchResult checks that a property reference fallback that fails after
// the deadline leaves the references of the finished search in the result and counts the target file as not
// processed.
func (s *inMemorySearchSuite) TestPropertyFallbackErrorAfterDeadlineKeepsSearchResult() {
	files := fixtureFiles()
	files["b.php"] = usageFixture("B")
	service, request := s.newService(files, fixtureAnswer(nil), nil, nil)
	service.runPropertyReferences = func(ctx context.Context, _, _, _, _ string) ([]phpactorReferenceRow, error) {
		<-ctx.Done()

		return nil, ctx.Err()
	}

	result, err := service.FindReferencingSymbols(deadlineContext(s.T()), request)
	s.Require().NoError(err)

	s.Require().Len(result.Symbols, 1)
	s.Equal("Fixture/read", result.Symbols[0].Path)
	s.Equal([]string{".php: 1 file is not processed: search stopped by timeout"}, result.Warnings)
}

// newConstantSearch writes a.php, b.php, and c.php with the constant STAMP into a new workspace and creates a phpactor
// service whose language server returns this constant for each document, and whose shared search opens documents
// through the callback that wrap builds from the default one. It returns the workspace search for STAMP.
func (s *inMemorySearchSuite) newConstantSearch(
	wrap func(stdlsp.WithRequestDocumentFunc) stdlsp.WithRequestDocumentFunc,
) (*Service, *domain.FindSymbolRequest) {
	constantFile := "<?php\nconst STAMP = 1;\n"
	service, referenceRequest := s.newService(
		map[string]string{
			"a.php": constantFile,
			"b.php": constantFile,
			"c.php": constantFile,
		},
		func(method string, _ string, _ int) (any, error) {
			if method != protocol.MethodTextDocumentDocumentSymbol {
				return nil, errors.New("method is not supported by the test language server")
			}

			return []protocol.DocumentSymbol{{
				Name:           "STAMP",
				Detail:         nil,
				Kind:           protocol.SymbolKindConstant,
				Tags:           nil,
				Deprecated:     nil,
				Range:          fixtureRange(1, 0, 1, 16),
				SelectionRange: fixtureRange(1, 6, 1, 11),
				Children:       nil,
			}}, nil
		},
		nil,
		nil,
	)
	s.wrapSharedService(service, wrap)

	return service, &domain.FindSymbolRequest{
		Path:              "STAMP",
		IncludeKinds:      nil,
		ExcludeKinds:      nil,
		Depth:             0,
		IncludeBody:       false,
		IncludeInfo:       false,
		SubstringMatching: false,
		WorkspaceRoot:     referenceRequest.WorkspaceRoot,
		Scope:             "",
	}
}

// wrapSharedService replaces the shared search of service with one that opens documents through the callback that
// wrap builds from the default one of service.
func (s *inMemorySearchSuite) wrapSharedService(
	service *Service,
	wrap func(stdlsp.WithRequestDocumentFunc) stdlsp.WithRequestDocumentFunc,
) {
	std, err := stdlsp.New(&stdlsp.Config{
		Extensions:                   extensions,
		EnsureConn:                   service.ensureConn,
		WithRequestDocument:          wrap(service.withRequestDocument),
		OpenFileForDocumentSymbol:    true,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    shouldIgnoreDir,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	})
	s.Require().NoError(err)
	service.std = std
}

// deadlineContext returns a request context of the test t whose deadline comes soon enough for the test to wait for
// it and late enough for the in-memory document requests before the waiting one.
func deadlineContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	t.Cleanup(cancel)

	return ctx
}

// foundFiles returns the file of each symbol of a symbol search result in order.
func foundFiles(result domain.FindSymbolResult) []string {
	files := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		files = append(files, symbol.File)
	}

	return files
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

const (
	// indexingToken is the token of the announced indexing progress of a test session.
	indexingToken = "indexing"
	// otherProgressToken is the token of a progress that the session does not announce, as phpactor does for a
	// reference search.
	otherProgressToken = "references"
	// syncProgressToken is the token of an announced progress that never begins.
	syncProgressToken = "sync"
	// shortIndexingStartTimeout is the time within which a test session must begin a progress, shorter than the
	// deadline of deadlineContext.
	shortIndexingStartTimeout = 100 * time.Millisecond
)

// newService writes files into a new workspace and creates a phpactor service whose in-memory language server answers
// through answer and has reported the begin and the end of indexing, whose member-reference command returns
// propertyRows, and whose session ends after the references request when sessionErr is set.
func (s *inMemorySearchSuite) newService(
	files map[string]string,
	answer testAnswer,
	sessionErr error,
	propertyRows []phpactorReferenceRow,
) (*Service, *domain.FindReferencingSymbolsRequest) {
	service, request, server := s.newServiceWithServer(files, answer, sessionErr, propertyRows)
	s.createProgress(server, indexingToken)
	s.notifyProgress(server, indexingToken, progressKindBegin)
	s.notifyProgress(server, indexingToken, progressKindEnd)

	return service, request
}

// newServiceWithServer creates the service of newService in a new session before any progress and also returns the
// server side of the connection, whose messages reach the server callback of the service.
func (s *inMemorySearchSuite) newServiceWithServer(
	files map[string]string,
	answer testAnswer,
	sessionErr error,
	propertyRows []phpactorReferenceRow,
) (*Service, *domain.FindReferencingSymbolsRequest, jsonrpc2.Conn) {
	t := s.T()
	// The runtime gives the server callback the workspace root without symbolic links.
	workspaceRoot, err := filepath.EvalSymlinks(t.TempDir())
	s.Require().NoError(err)
	for name, content := range files {
		s.Require().NoError(os.WriteFile(filepath.Join(workspaceRoot, name), []byte(content), 0o600))
	}

	var (
		referenced atomic.Bool
		service    *Service
	)
	conn, server := s.newConn(
		func(method string, documentPath string, call int) (any, error) {
			if method == protocol.MethodTextDocumentReferences {
				referenced.Store(true)
			}

			return answer(method, documentPath, call)
		},
		func(ctx context.Context, req *jsonrpc2.Request) (any, bool, error) {
			result, handled, err := service.handleServerCallback(ctx, req, workspaceRoot)
			// The runtime answers window/workDoneProgress/create with null after the server callback.
			if !handled && err == nil && req.Method() == protocol.MethodWindowWorkDoneProgressCreate {
				return nil, true, nil
			}

			return result, handled, err
		},
	)

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

	service = &Service{
		rt:                  rt,
		ensureConn:          ensureConn,
		std:                 std,
		withRequestDocument: withRequestDocument,
		runPropertyReferences: func(context.Context, string, string, string, string) ([]phpactorReferenceRow, error) {
			return propertyRows, nil
		},
		cacheRoot:            t.TempDir(),
		indexingStartTimeout: phpactorIndexingStartTimeout,
		mu:                   sync.Mutex{},
		indexing:             make(map[string]*indexingState),
	}
	// The runtime calls this hook before the initialize request of each new session.
	s.Require().NoError(service.patchInitializeParams(workspaceRoot, &protocol.InitializeParams{}))

	return service, &domain.FindReferencingSymbolsRequest{
		Path:          "Fixture/value",
		IncludeKinds:  nil,
		ExcludeKinds:  nil,
		WorkspaceRoot: workspaceRoot,
		File:          "fixture.php",
	}, server
}

// createProgress sends the window/workDoneProgress/create request with token from the in-memory language server and
// returns after the client has answered it.
func (s *inMemorySearchSuite) createProgress(server jsonrpc2.Conn, token string) {
	_, err := server.Call(
		s.T().Context(),
		protocol.MethodWindowWorkDoneProgressCreate,
		map[string]any{"token": token},
		nil,
	)
	s.Require().NoError(err)
}

// notifyProgress sends the $/progress notification of kind with token from the in-memory language server and returns
// after the client has handled it.
func (s *inMemorySearchSuite) notifyProgress(server jsonrpc2.Conn, token string, kind string) {
	s.Require().NoError(server.Notify(s.T().Context(), protocol.MethodProgress, map[string]any{
		"token": token,
		"value": map[string]any{"kind": kind},
	}))
	// The client handles the messages of the server in their order, so the answer to this request, which announces a
	// progress that never begins, comes after the client has handled the notification.
	s.createProgress(server, syncProgressToken)
}

// newConn builds an in-memory language server that accepts document notifications and answers each request through
// answer, and returns the client side, which passes each message from the server to callback first, and the server
// side.
func (s *inMemorySearchSuite) newConn(
	answer testAnswer,
	callback func(ctx context.Context, req *jsonrpc2.Request) (result any, handled bool, err error),
) (client, server jsonrpc2.Conn) {
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
	clientConn.Go(t.Context(), func(ctx context.Context, req *jsonrpc2.Request) (any, error) {
		result, handled, err := callback(ctx, req)
		if handled || err != nil {
			return result, err
		}

		return jsonrpc2.MethodNotFoundHandler(ctx, req)
	})
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

	return clientConn, serverConn
}

// waitForDeadline wraps withRequestDocument so that each document request with the file fileName waits for the end of
// the request context and returns its error.
func waitForDeadline(
	withRequestDocument stdlsp.WithRequestDocumentFunc,
	fileName string,
) stdlsp.WithRequestDocumentFunc {
	return func(ctx context.Context, conn jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		if filepath.Base(absolutePath) != fileName {
			return withRequestDocument(ctx, conn, absolutePath, run)
		}
		<-ctx.Done()

		return ctx.Err()
	}
}

// finishAfterDeadline wraps withRequestDocument so that each document request runs and, when requests is set, is
// counted in it, and the document request number fileCall of the file fileName waits for the end of the request
// context after it runs.
func finishAfterDeadline(
	withRequestDocument stdlsp.WithRequestDocumentFunc,
	fileName string,
	fileCall int32,
	requests *atomic.Int32,
) stdlsp.WithRequestDocumentFunc {
	var fileCalls atomic.Int32

	return func(ctx context.Context, conn jsonrpc2.Conn, absolutePath string, run func(context.Context) error) error {
		if requests != nil {
			requests.Add(1)
		}
		err := withRequestDocument(ctx, conn, absolutePath, run)
		if filepath.Base(absolutePath) == fileName && fileCalls.Add(1) == fileCall {
			<-ctx.Done()
		}

		return err
	}
}

// propertyAnswer answers documentSymbol with the class tree of fixture.php or with the symbols that usageSymbols holds
// for the base name of the document, and references with the usage of the property in the method read of
// fixture.php.
func propertyAnswer(usageSymbols map[string][]protocol.DocumentSymbol) testAnswer {
	return func(method string, documentPath string, _ int) (any, error) {
		switch method {
		case protocol.MethodTextDocumentDocumentSymbol:
			if symbols, ok := usageSymbols[filepath.Base(documentPath)]; ok {
				return symbols, nil
			}

			return fixtureSymbols(), nil
		case protocol.MethodTextDocumentReferences:
			return []protocol.Location{{URI: uri.File(documentPath), Range: fixtureRange(3, 43, 3, 48)}}, nil
		default:
			return nil, errors.New("method is not supported by the test language server")
		}
	}
}

// methodSymbol returns the document symbol of a one-line method with the given name on the given line.
func methodSymbol(name string, line uint32) protocol.DocumentSymbol {
	return protocol.DocumentSymbol{
		Name:           name,
		Detail:         nil,
		Kind:           protocol.SymbolKindMethod,
		Tags:           nil,
		Deprecated:     nil,
		Range:          fixtureRange(line, 4, line, 70),
		SelectionRange: fixtureRange(line, 20, line, 20+uint32(len(name))),
		Children:       nil,
	}
}

// referencingPaths returns the path of each row of a reference search result in order.
func referencingPaths(result domain.FindReferencingSymbolsResult) []string {
	paths := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		paths = append(paths, symbol.Path)
	}

	return paths
}

// usageAnswer answers documentSymbol with the class tree of fixture.php or of the usage file B, and references with
// the usage of the property in b.php.
func usageAnswer() testAnswer {
	return func(method string, documentPath string, _ int) (any, error) {
		switch method {
		case protocol.MethodTextDocumentDocumentSymbol:
			if filepath.Base(documentPath) == "b.php" {
				return usageSymbols("B"), nil
			}

			return fixtureSymbols(), nil
		case protocol.MethodTextDocumentReferences:
			bPath := filepath.Join(filepath.Dir(documentPath), "b.php")

			return []protocol.Location{{URI: uri.File(bPath), Range: fixtureRange(2, 63, 2, 68)}}, nil
		default:
			return nil, errors.New("method is not supported by the test language server")
		}
	}
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
