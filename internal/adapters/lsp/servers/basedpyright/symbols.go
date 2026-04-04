package lspbasedpyright

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
	return s.std.GetSymbolsOverview(ctx, request)
}

// FindSymbol delegates canonical path matching to the shared standard-LSP search flow.
func (s *Service) FindSymbol(
	ctx context.Context,
	request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	return s.std.FindSymbol(ctx, request)
}

// FindReferencingSymbols keeps the target-directory Python file set open for the whole shared workflow so
// basedpyright can resolve cross-file references before stdlsp groups the final result.
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
	err = helpers.RunWithReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		referenceWorkflowFiles,
		s.withRequestDocument,
		func(callCtx context.Context) error {
			var callErr error
			result, callErr = s.std.FindReferencingSymbols(callCtx, request)

			return callErr
		},
	)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	return result, nil
}

// shouldIgnoreDir filters directories that add Python analysis noise or unnecessary filesystem cost.
func shouldIgnoreDir(relativePath string) bool {
	baseName := filepath.Base(relativePath)
	if strings.HasPrefix(baseName, ".") {
		return true
	}

	switch baseName {
	case "__pycache__", "venv", ".venv", ".env", ".pixi", "build", "dist":
		return true
	default:
		return false
	}
}
