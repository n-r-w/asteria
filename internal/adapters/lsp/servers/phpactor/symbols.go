package lspphpactor

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// GetSymbolsOverview delegates the standard document-symbol workflow to stdlsp.
func (s *Service) GetSymbolsOverview(
	ctx context.Context,
	request *domain.GetSymbolsOverviewRequest,
) (domain.GetSymbolsOverviewResult, error) {
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
		logFallbackWarning(ctx, "skip phpactor overview constant fallback", augmentErr, "file_path", request.File)

		return result, nil
	}

	return augmentedResult, nil
}

// FindSymbol delegates canonical path matching to the shared standard-LSP search flow.
func (s *Service) FindSymbol(
	ctx context.Context,
	request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	result, err := s.std.FindSymbol(ctx, request)
	if err != nil || request == nil {
		return result, err
	}

	workspaceRoot, rootErr := helpers.ResolveWorkspaceRoot(request.WorkspaceRoot)
	if rootErr != nil {
		return domain.FindSymbolResult{}, rootErr
	}

	augmentedResult, augmentErr := augmentFindSymbolWithPHPConstants(
		workspaceRoot,
		request,
		result,
	)
	if augmentErr != nil {
		logFallbackWarning(
			ctx,
			"skip phpactor find_symbol constant fallback",
			augmentErr,
			"scope_path", request.Scope,
			"symbol_path", request.Path,
		)
	} else {
		result = augmentedResult
	}
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

// FindReferencingSymbols keeps one bounded same-directory PHP file set open so phpactor can resolve nearby
// cross-file references without the cost of recursively opening nested workspace trees.
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

	indexErr := buildPHPActorIndex(ctx, s.cacheRoot, workspaceRoot)
	if indexErr != nil {
		return domain.FindReferencingSymbolsResult{}, indexErr
	}

	result, err := s.stdReferences.FindReferencingSymbols(ctx, request)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	augmentedResult, augmentErr := s.augmentPropertyReferenceResults(
		ctx,
		workspaceRoot,
		request,
		result,
	)
	if augmentErr != nil {
		logFallbackWarning(
			ctx,
			"skip phpactor property reference fallback",
			augmentErr,
			"file_path", request.File,
			"symbol_path", request.Path,
		)

		return result, nil
	}

	return augmentedResult, nil
}

// patchInitializeParams keeps the deprecated startup workaround local to phpactor while disabling helper
// integrations that add noise to symbolic-search startup and redirecting Phpactor state into the managed cache.
func (s *Service) patchInitializeParams(workspaceRoot string, params *protocol.InitializeParams) error {
	indexPath, err := phpactorIndexerPath(s.cacheRoot, workspaceRoot)
	if err != nil {
		return err
	}

	//nolint:staticcheck // Phpactor initialize does not answer in this environment unless RootURI is set.
	params.RootURI = uri.File(workspaceRoot)
	params.InitializationOptions = map[string]any{
		phpactorIndexerPathKey:       indexPath,
		phpactorPHPStanEnabledKey:    false,
		phpactorPsalmEnabledKey:      false,
		phpactorPHPCSFixerEnabledKey: false,
	}

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
	case "node_modules", "cache", "vendor":
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
