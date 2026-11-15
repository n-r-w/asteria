package lspphpactor

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/domain"
)

// GetSymbolsOverview delegates the standard document-symbol workflow to stdlsp.
func (s *Service) GetSymbolsOverview(
	ctx context.Context,
	request *domain.GetSymbolsOverviewRequest,
) (domain.GetSymbolsOverviewResult, error) {
	if request != nil {
		acquired, acquireErr := s.rt.AcquireClosableSession(ctx, request.WorkspaceRoot)
		if acquireErr != nil {
			return domain.GetSymbolsOverviewResult{}, acquireErr
		}
		defer releaseSessionAfterRequest(ctx, acquired)
		// Each connection lookup of the request uses the acquired session, or fails after this session was closed.
		ctx = runtimelsp.WithAcquiredSession(ctx, acquired)
	}

	result, err := s.std.GetSymbolsOverview(ctx, request)
	if err != nil || request == nil {
		return result, err
	}

	workspaceRoot, rootErr := helpers.ResolveWorkspaceRoot(request.WorkspaceRoot)
	if rootErr != nil {
		return domain.GetSymbolsOverviewResult{}, rootErr
	}

	augmentedResult, augmentErr := augmentOverviewWithPHPConstants(workspaceRoot, request, result)
	if augmentErr != nil {
		if fatalErr := handleFallbackError(
			ctx,
			"skip phpactor overview constant fallback",
			augmentErr,
			"file_path", request.File,
		); fatalErr != nil {
			return domain.GetSymbolsOverviewResult{}, fatalErr
		}

		return result, nil
	}

	return augmentedResult, nil
}

// FindSymbol delegates canonical path matching to the shared standard-LSP search flow.
// Warnings of the result name the files of a directory or workspace search that could not be processed, and count
// the files not searched because the search or the constant fallback stopped at the request deadline.
func (s *Service) FindSymbol(
	ctx context.Context,
	request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	if request != nil {
		acquired, acquireErr := s.rt.AcquireClosableSession(ctx, request.WorkspaceRoot)
		if acquireErr != nil {
			return domain.FindSymbolResult{}, acquireErr
		}
		defer releaseSessionAfterRequest(ctx, acquired)
		// Each connection lookup of the request uses the acquired session, or fails after this session was closed.
		ctx = runtimelsp.WithAcquiredSession(ctx, acquired)
	}

	result, failures, err := s.std.FindSymbolWithFailures(ctx, request)
	if err != nil || request == nil {
		return result, err
	}

	workspaceRoot, rootErr := helpers.ResolveWorkspaceRoot(request.WorkspaceRoot)
	if rootErr != nil {
		return domain.FindSymbolResult{}, rootErr
	}

	// After the deadline, the search returns what it found, without the constant fallback.
	if phpConstantFallbackApplies(request) && !failures.StopFollowUpByTimeout(ctx) {
		result, err = addPHPConstants(ctx, workspaceRoot, request, result, failures)
		if err != nil {
			return domain.FindSymbolResult{}, err
		}
	}
	failures.CompleteSymbolResult(&result)
	if !request.IncludeInfo || len(result.Symbols) == 0 {
		return result, nil
	}

	for i := range result.Symbols {
		normalizedInfo, normalizeErr := normalizeFoundSymbolInfo(workspaceRoot, &result.Symbols[i])
		if normalizeErr != nil {
			slog.WarnContext(
				ctx,
				"skip phpactor-specific info fallback",
				"err", normalizeErr,
				"file_path", result.Symbols[i].File,
				"symbol_path", result.Symbols[i].Path,
			)

			continue
		}

		result.Symbols[i].Info = normalizedInfo
	}

	return result, nil
}

// addPHPConstants adds the top-level constants that phpactor omits to the result of a finished directory, workspace,
// or file search, and records in failures the files whose constants the fallback could not read. In a directory or
// workspace scope, the fallback stops at the request deadline and records the current file and the files after it as
// not searched, or records each PHP file of the scope as not searched when it fails after the deadline.
func addPHPConstants(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindSymbolRequest,
	result domain.FindSymbolResult,
	failures *stdlsp.FileFailures,
) (domain.FindSymbolResult, error) {
	augmentedResult, augmentErr := augmentFindSymbolWithPHPConstants(
		workspaceRoot,
		request,
		result,
		func(relativePath string, fileErr error) error {
			return failures.Record(ctx, relativePath, fileErr)
		},
		func(notSearched []string) bool {
			return failures.StopByTimeout(ctx, notSearched)
		},
	)
	if augmentErr == nil {
		return augmentedResult, nil
	}
	// When the fallback of a directory or workspace search failed after the deadline, the result keeps the symbols of
	// the finished search, and the PHP files of the scope count as not searched because their constants are left out.
	if failures.StopFollowUpByTimeout(ctx) {
		return result, nil
	}
	if fatalErr := handleFallbackError(
		ctx,
		"skip phpactor find_symbol constant fallback",
		augmentErr,
		"scope_path", request.Scope,
		"symbol_path", request.Path,
	); fatalErr != nil {
		return domain.FindSymbolResult{}, fatalErr
	}

	return result, nil
}

// FindReferencingSymbols waits until the phpactor session finishes indexing the workspace and then returns the
// references from all indexed files; when ctx ends during the wait, it returns the error of ctx.
// Warnings of the result name the files with references that could not be processed, and count the files not processed
// because the search or the property reference fallback stopped at the request deadline.
func (s *Service) FindReferencingSymbols(
	ctx context.Context,
	request *domain.FindReferencingSymbolsRequest,
) (domain.FindReferencingSymbolsResult, error) {
	if request == nil {
		return domain.FindReferencingSymbolsResult{}, errors.New("request is required")
	}
	if err := request.Validate(); err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	workspaceRoot, err := helpers.ResolveWorkspaceRoot(request.WorkspaceRoot)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}
	acquired, acquireErr := s.rt.AcquireClosableSession(ctx, workspaceRoot)
	if acquireErr != nil {
		return domain.FindReferencingSymbolsResult{}, acquireErr
	}
	// Each connection lookup of the request, including the one before the wait for indexing, uses the acquired
	// session, or fails after this session was closed.
	ctx = runtimelsp.WithAcquiredSession(ctx, acquired)
	// A request that ends while it waits for indexing has sent phpactor no search request, so the session stays open
	// and keeps indexing for later requests.
	endedDuringIndexing := false
	defer func() {
		if endedDuringIndexing {
			acquired.Release()

			return
		}
		releaseSessionAfterRequest(ctx, acquired)
	}()

	targetFile, targetPath, err := helpers.ResolveDocumentPath(workspaceRoot, request.File)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	conn, err := s.ensureConn(ctx, workspaceRoot)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}
	if err = s.waitForIndexing(ctx, workspaceRoot); err != nil {
		endedDuringIndexing = true

		return domain.FindReferencingSymbolsResult{}, err
	}

	var (
		result   domain.FindReferencingSymbolsResult
		failures *stdlsp.FileFailures
	)
	err = s.withRequestDocument(ctx, conn, targetPath, func(callCtx context.Context) error {
		var callErr error
		result, failures, callErr = s.std.FindReferencingSymbolsWithFailures(callCtx, request)

		return callErr
	})
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	// After the deadline, the search returns what it found, without the property reference fallback.
	if !failures.StoppedByTimeout() && !failures.StopByTimeout(ctx, []string{targetFile}) {
		result, err = s.addPropertyReferences(ctx, workspaceRoot, request, result, targetFile, failures)
		if err != nil {
			return domain.FindReferencingSymbolsResult{}, err
		}
	}
	failures.CompleteReferenceResult(&result)

	return result, nil
}

// addPropertyReferences adds the property references that the phpactor CLI finds to the result of a finished
// reference search, and records in failures the files that the fallback could not process. At the request deadline,
// the fallback keeps the rows that it finished and records the files of its other rows as not processed; when it fails
// after the deadline before its rows, targetFile counts as not processed.
func (s *Service) addPropertyReferences(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindReferencingSymbolsRequest,
	result domain.FindReferencingSymbolsResult,
	targetFile string,
	failures *stdlsp.FileFailures,
) (domain.FindReferencingSymbolsResult, error) {
	augmentedResult, augmentErr := s.augmentPropertyReferenceResults(
		ctx,
		workspaceRoot,
		request,
		result,
		func(relativePath string, fileErr error) error {
			return failures.Record(ctx, relativePath, fileErr)
		},
		func(notProcessed []string) bool {
			return failures.StopByTimeout(ctx, notProcessed)
		},
	)
	if augmentErr == nil {
		return augmentedResult, nil
	}
	// The result keeps the references of the finished search when the fallback failed after the deadline.
	if failures.StopByTimeout(ctx, []string{targetFile}) {
		return result, nil
	}
	if fatalErr := handleFallbackError(
		ctx,
		"skip phpactor property reference fallback",
		augmentErr,
		"file_path", request.File,
		"symbol_path", request.Path,
	); fatalErr != nil {
		return domain.FindReferencingSymbolsResult{}, fatalErr
	}

	return result, nil
}

// patchInitializeParams sets the phpactor options of a new session and makes reference searches wait for the
// indexing of this session.
func (s *Service) patchInitializeParams(workspaceRoot string, params *protocol.InitializeParams) error {
	// The runtime calls this hook before it sends the initialize request, so the new session cannot report the end of
	// indexing before startIndexing runs.
	s.startIndexing(workspaceRoot)

	indexPath, err := phpactorIndexerPath(s.cacheRoot, workspaceRoot)
	if err != nil {
		return err
	}

	//nolint:staticcheck // Phpactor initialize does not answer in this environment unless RootURI is set.
	params.RootURI = new(uri.File(workspaceRoot))
	initializationOptions, err := helpers.MarshalLSPAny(map[string]any{
		phpactorIndexerPathKey:         indexPath,
		phpactorEnabledWatchersKey:     []string{phpactorLSPWatcher},
		phpactorPHPStanEnabledKey:      false,
		phpactorPsalmEnabledKey:        false,
		phpactorPHPCSFixerEnabledKey:   false,
		phpactorAutoConfigKey:          false,
		phpactorDiagnosticsOnOpenKey:   false,
		phpactorDiagnosticsOnUpdateKey: false,
		phpactorDiagnosticsOnSaveKey:   false,
	})
	if err != nil {
		return err
	}
	params.InitializationOptions = initializationOptions

	return nil
}

// phpactorIndexerPath keeps one workspace-local Phpactor index under the managed adapter cache so external
// workspaces never receive hidden service directories from Asteria.
func phpactorIndexerPath(cacheRoot, workspaceRoot string) (string, error) {
	adapterCacheDir, err := helpers.AdapterCacheDir(cacheRoot, workspaceRoot, phpactorServerName)
	if err != nil {
		return "", err
	}

	return filepath.Join(adapterCacheDir, phpactorIndexerDirName), nil
}

// ensureIndexerPathExists creates the managed Phpactor index directory before reference requests touch it.
func ensureIndexerPathExists(cacheRoot, workspaceRoot string) error {
	indexPath, err := phpactorIndexerPath(cacheRoot, workspaceRoot)
	if err != nil {
		return err
	}

	return os.MkdirAll(indexPath, phpactorStateDirPermissions)
}

// shouldIgnoreDir filters directories that add PHP analysis noise or unnecessary filesystem cost.
func shouldIgnoreDir(relativePath string) bool {
	baseName := filepath.Base(relativePath)
	if strings.HasPrefix(baseName, ".") {
		return true
	}

	switch baseName {
	case "node_modules", "cache":
		return true
	default:
		return false
	}
}

// normalizeFoundSymbolInfo replaces phpactor hover placeholders with a stable declaration line so `info`
// stays useful even when the server responds with a class-level source lookup error.
func normalizeFoundSymbolInfo(workspaceRoot string, symbol *domain.FoundSymbol) (string, error) {
	trimmedInfo := strings.TrimSpace(symbol.Info)
	if !shouldFallbackFoundSymbolInfo(trimmedInfo) {
		return trimmedInfo, nil
	}

	fallbackInfo, err := fallbackFoundSymbolInfo(workspaceRoot, symbol)
	if err != nil {
		return "", err
	}
	if fallbackInfo == "" {
		return "", nil
	}

	return fallbackInfo, nil
}

// shouldFallbackFoundSymbolInfo identifies phpactor hover payloads that read like transport-successful
// errors instead of user-meaningful symbol info.
func shouldFallbackFoundSymbolInfo(info string) bool {
	trimmedInfo := strings.TrimSpace(info)

	return trimmedInfo == "" || strings.HasPrefix(trimmedInfo, "Could not find source with ")
}

// fallbackFoundSymbolInfo extracts one declaration line from the symbol range so PHP callers still get a
// concise, file-backed description when phpactor hover metadata is missing or unusable.
func fallbackFoundSymbolInfo(workspaceRoot string, symbol *domain.FoundSymbol) (string, error) {
	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, symbol.File)
	if err != nil {
		return "", err
	}

	safeAbsolutePath := filepath.Clean(absolutePath)
	fileContent, err := os.ReadFile(safeAbsolutePath)
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.ReplaceAll(string(fileContent), "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return "", nil
	}

	startLine := max(0, symbol.StartLine)
	endLine := min(len(lines)-1, symbol.EndLine)
	if startLine > endLine {
		return "", nil
	}

	searchToken := foundSymbolInfoSearchToken(symbol.Path)
	for lineIndex := startLine; lineIndex <= endLine; lineIndex++ {
		trimmedLine := strings.TrimSpace(lines[lineIndex])
		if trimmedLine == "" {
			continue
		}
		if searchToken != "" && strings.Contains(trimmedLine, searchToken) {
			return trimmedLine, nil
		}
	}

	for lineIndex := startLine; lineIndex <= endLine; lineIndex++ {
		trimmedLine := strings.TrimSpace(lines[lineIndex])
		if trimmedLine != "" {
			return trimmedLine, nil
		}
	}

	return "", nil
}

// foundSymbolInfoSearchToken derives the leaf name from one canonical symbol path so fallback extraction can
// prefer declaration lines that mention the actual symbol name.
func foundSymbolInfoSearchToken(symbolPath string) string {
	trimmedPath := strings.TrimSpace(symbolPath)
	if trimmedPath == "" {
		return ""
	}

	pathComponents := strings.Split(trimmedPath, "/")
	leaf := pathComponents[len(pathComponents)-1]
	if discriminatorIndex := strings.IndexByte(leaf, '@'); discriminatorIndex >= 0 {
		leaf = leaf[:discriminatorIndex]
	}

	return leaf
}
