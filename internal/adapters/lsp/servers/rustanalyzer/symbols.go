package lsprustanalyzer

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/domain"
)

// GetSymbolsOverview delegates the standard document-symbol workflow to stdlsp.
func (s *Service) GetSymbolsOverview(
	ctx context.Context,
	request *domain.GetSymbolsOverviewRequest,
) (domain.GetSymbolsOverviewResult, error) {
	if request != nil {
		release, acquireErr := s.rt.AcquireSession(ctx, request.WorkspaceRoot)
		if acquireErr != nil {
			return domain.GetSymbolsOverviewResult{}, acquireErr
		}
		defer release()
	}

	result, err := s.std.GetSymbolsOverview(ctx, request)
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}

	normalizeOverviewSymbolPaths(result.Symbols)

	return result, nil
}

// FindSymbol uses the shared standard-LSP search flow and retries Rust impl-member queries through the raw
// rust-analyzer path when the canonical export shape does not match directly. Warnings of the result name the files
// of a directory or workspace search that either attempt could not process.
func (s *Service) FindSymbol(
	ctx context.Context,
	request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	if request != nil {
		release, acquireErr := s.rt.AcquireSession(ctx, request.WorkspaceRoot)
		if acquireErr != nil {
			return domain.FindSymbolResult{}, acquireErr
		}
		defer release()
	}

	result, failures, err := s.std.FindSymbolWithFailures(ctx, request)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}
	// Symbols of failed files are removed first, so only the usable symbols decide whether to retry.
	failures.CompleteSymbolResult(&result)
	if len(result.Symbols) == 0 {
		rawRequest, changed := findSymbolRequestWithRawRustPath(request)
		if changed {
			rawResult, rawFailures, rawErr := s.std.FindSymbolWithFailures(ctx, rawRequest)
			if rawErr != nil {
				return domain.FindSymbolResult{}, rawErr
			}
			failures.Merge(rawFailures)
			result = rawResult
			failures.CompleteSymbolResult(&result)
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
	if request != nil {
		release, acquireErr := s.rt.AcquireSession(ctx, request.WorkspaceRoot)
		if acquireErr != nil {
			return domain.FindReferencingSymbolsResult{}, acquireErr
		}
		defer release()
	}

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

	conn, err := s.ensureConn(ctx, workspaceRoot)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	var result domain.FindReferencingSymbolsResult
	// failures holds every file that this request skipped, so that the raw retry and the final result leave out the
	// same files.
	failures := stdlsp.NewFileFailures(workspaceRoot, false)
	rawRequest, hasRawVariant := findReferencingSymbolsRequestWithRawRustPath(request)
	skipped, err := helpers.RunWithReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		referenceWorkflowFiles,
		s.withRequestDocument,
		func(callCtx context.Context, _ []string, knownSkipped []helpers.SkippedFile) error {
			if recordErr := failures.RecordSkippedFiles(callCtx, knownSkipped); recordErr != nil {
				return recordErr
			}

			var callErr error
			result, callErr = s.findReferencingSymbolsOnce(
				callCtx,
				request,
				rawRequest,
				hasRawVariant,
				failures,
			)

			return callErr
		},
	)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}
	// The files that failed to close after the workflow are known only now; the files recorded earlier count once.
	if recordErr := failures.RecordSkippedFiles(ctx, skipped); recordErr != nil {
		return domain.FindReferencingSymbolsResult{}, recordErr
	}
	failures.CompleteReferenceResult(&result)

	normalizeReferencingSymbolPaths(result.Symbols)

	return result, nil
}

// findReferencingSymbolsOnce runs one standard reference lookup and, when needed, one raw impl-path retry for
// canonical Rust member queries. It adds the files that each lookup could not process to failures, and returns the
// result of the last lookup without the rows of any file in failures.
func (s *Service) findReferencingSymbolsOnce(
	ctx context.Context,
	request *domain.FindReferencingSymbolsRequest,
	rawRequest *domain.FindReferencingSymbolsRequest,
	hasRawVariant bool,
	failures *stdlsp.FileFailures,
) (domain.FindReferencingSymbolsResult, error) {
	result, attemptFailures, err := s.stdReferences.FindReferencingSymbolsWithFailures(ctx, request)
	if err == nil {
		failures.Merge(attemptFailures)
		// Rows of failed files are removed first, so only the usable rows decide whether to retry.
		failures.CompleteReferenceResult(&result)
	}
	if !hasRawVariant {
		return result, err
	}
	if err == nil && len(result.Symbols) > 0 {
		return result, nil
	}
	if err != nil && !isNoSymbolMatchesError(err) {
		return result, err
	}

	rawResult, rawFailures, err := s.stdReferences.FindReferencingSymbolsWithFailures(ctx, rawRequest)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}
	failures.Merge(rawFailures)
	failures.CompleteReferenceResult(&rawResult)

	return rawResult, nil
}

// isNoSymbolMatchesError identifies the symbol miss after which the raw impl path is retried.
func isNoSymbolMatchesError(err error) bool {
	var toolErr *domain.ToolError
	if !errors.As(err, &toolErr) {
		return false
	}

	return strings.HasPrefix(toolErr.Error(), `no symbol matches `)
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
