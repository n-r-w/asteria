package lsprustanalyzer

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
)

// GetSymbolsOverview delegates the standard document-symbol workflow to stdlsp.
func (s *Service) GetSymbolsOverview(
	ctx context.Context,
	request *domain.GetSymbolsOverviewRequest,
) (domain.GetSymbolsOverviewResult, error) {
	result, err := s.std.GetSymbolsOverview(ctx, request)
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}

	normalizeOverviewSymbolPaths(result.Symbols)

	return result, nil
}

// FindSymbol uses the shared standard-LSP search flow and retries Rust impl-member queries through the raw
// rust-analyzer path when the canonical export shape does not match directly.
func (s *Service) FindSymbol(
	ctx context.Context,
	request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	result, err := s.std.FindSymbol(ctx, request)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}
	if len(result.Symbols) == 0 {
		rawRequest, changed := findSymbolRequestWithRawRustPath(request)
		if changed {
			result, err = s.std.FindSymbol(ctx, rawRequest)
			if err != nil {
				return domain.FindSymbolResult{}, err
			}
		}
	}

	normalizeFoundSymbolPaths(result.Symbols)

	return result, nil
}

// FindReferencingSymbols runs the shared reference workflow while retrying Rust impl-member queries through
// the raw rust-analyzer path before exporting normalized result paths.
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

	referenceWorkflowFiles, err := helpers.CollectReferenceWorkflowFiles(
		workspaceRoot,
		request.File,
		extensions,
		shouldIgnoreDir,
	)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	conn, err := s.rt.EnsureConn(ctx, workspaceRoot)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	var result domain.FindReferencingSymbolsResult
	rawRequest, hasRawVariant := findReferencingSymbolsRequestWithRawRustPath(request)
	err = helpers.RunWithReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		referenceWorkflowFiles,
		s.withRequestDocument,
		func(callCtx context.Context) error {
			var callErr error
			result, callErr = s.findReferencingSymbolsOnce(
				callCtx,
				request,
				rawRequest,
				hasRawVariant,
			)

			return callErr
		},
	)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	normalizeReferencingSymbolPaths(result.Symbols)

	return result, nil
}

// findReferencingSymbolsOnce runs one standard reference lookup and, when needed, one raw impl-path retry for
// canonical Rust member queries.
func (s *Service) findReferencingSymbolsOnce(
	ctx context.Context,
	request *domain.FindReferencingSymbolsRequest,
	rawRequest *domain.FindReferencingSymbolsRequest,
	hasRawVariant bool,
) (domain.FindReferencingSymbolsResult, error) {
	result, err := s.stdReferences.FindReferencingSymbols(ctx, request)
	if !hasRawVariant {
		return result, err
	}
	if err == nil && len(result.Symbols) > 0 {
		return result, nil
	}
	if err != nil && !isNoSymbolMatchesSafeError(err) {
		return result, err
	}

	return s.stdReferences.FindReferencingSymbols(ctx, rawRequest)
}

// isNoSymbolMatchesSafeError identifies the public-safe miss case where retrying the raw impl path is valid.
func isNoSymbolMatchesSafeError(err error) bool {
	var safeErr *domain.SafeError
	if !errors.As(err, &safeErr) {
		return false
	}

	return strings.HasPrefix(safeErr.Error(), `no symbol matches `)
}

// shouldIgnoreDir filters directories that add Rust analysis noise or unnecessary filesystem cost.
func shouldIgnoreDir(relativePath string) bool {
	baseName := filepath.Base(relativePath)
	if strings.HasPrefix(baseName, ".") {
		return true
	}

	switch baseName {
	case rustTargetDirName:
		return true
	default:
		return false
	}
}

// normalizeOverviewSymbolPaths rewrites raw rust-analyzer impl-member paths into the canonical exported form.
func normalizeOverviewSymbolPaths(symbols []domain.SymbolLocation) {
	for symbolIndex := range symbols {
		symbols[symbolIndex].Path = normalizeExportedRustNamePath(symbols[symbolIndex].Path)
	}
}

// normalizeFoundSymbolPaths rewrites raw rust-analyzer impl-member paths into the canonical exported form.
func normalizeFoundSymbolPaths(symbols []domain.FoundSymbol) {
	for symbolIndex := range symbols {
		symbols[symbolIndex].Path = normalizeExportedRustNamePath(symbols[symbolIndex].Path)
	}
}

// normalizeReferencingSymbolPaths rewrites raw rust-analyzer impl-member paths into the canonical exported form.
func normalizeReferencingSymbolPaths(symbols []domain.ReferencingSymbol) {
	for symbolIndex := range symbols {
		symbols[symbolIndex].Path = normalizeExportedRustNamePath(symbols[symbolIndex].Path)
	}
}

// normalizeExportedRustNamePath trims the impl prefix from container components while leaving standalone impl
// blocks unchanged, so exported search paths stay stable and queryable.
func normalizeExportedRustNamePath(namePath string) string {
	components := strings.Split(strings.TrimSpace(namePath), "/")
	if len(components) <= 1 {
		return namePath
	}

	for componentIndex := range components[:len(components)-1] {
		components[componentIndex] = strings.TrimPrefix(components[componentIndex], rustImplSymbolPrefix)
	}

	return strings.Join(components, "/")
}

// findSymbolRequestWithRawRustPath retries canonical Rust member queries through rust-analyzer's raw impl path
// only when the incoming query shape can represent an impl member.
func findSymbolRequestWithRawRustPath(request *domain.FindSymbolRequest) (*domain.FindSymbolRequest, bool) {
	rawPath, changed := rawRustQueryPath(request.Path)
	if !changed {
		return request, false
	}

	rawRequest := *request
	rawRequest.Path = rawPath

	return &rawRequest, true
}

// findReferencingSymbolsRequestWithRawRustPath retries canonical Rust member queries through rust-analyzer's raw
// impl path only when the incoming query shape can represent an impl member.
func findReferencingSymbolsRequestWithRawRustPath(
	request *domain.FindReferencingSymbolsRequest,
) (*domain.FindReferencingSymbolsRequest, bool) {
	rawPath, changed := rawRustQueryPath(request.Path)
	if !changed {
		return request, false
	}

	rawRequest := *request
	rawRequest.Path = rawPath

	return &rawRequest, true
}

// rawRustQueryPath converts a canonical Rust member query into rust-analyzer's raw impl-container variant by
// prefixing the final container component, which is the part exported-path normalization strips on the way out.
func rawRustQueryPath(namePath string) (string, bool) {
	trimmedPath := strings.TrimSpace(namePath)
	components := strings.Split(trimmedPath, "/")
	if len(components) < rustMemberPathComponentCount {
		return namePath, false
	}

	containerIndex := len(components) - rustMemberPathComponentCount
	if strings.HasPrefix(components[containerIndex], rustImplSymbolPrefix) {
		return namePath, false
	}

	components[containerIndex] = rustImplSymbolPrefix + components[containerIndex]

	return strings.Join(components, "/"), true
}
