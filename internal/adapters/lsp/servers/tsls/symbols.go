package lsptsls

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/domain"
	"go.lsp.dev/jsonrpc2"
)

// FindReferencingSymbols keeps the target-directory file set open for the whole shared workflow so
// tsls can resolve cross-file references before stdlsp groups the final result.
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

	referenceWorkflowFiles, err := collectReferenceWorkflowFiles(workspaceRoot, request.File)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	conn, err := s.rt.EnsureConn(ctx, workspaceRoot)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	var result domain.FindReferencingSymbolsResult
	err = runWithReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		referenceWorkflowFiles,
		newWithRequestDocument(),
		func(callCtx context.Context) error {
			resolvedRequest, resolveErr := s.resolveReferenceTarget(callCtx, workspaceRoot, request)
			if resolveErr != nil {
				return resolveErr
			}

			var callErr error
			result, callErr = s.Service.FindReferencingSymbols(callCtx, resolvedRequest)

			return callErr
		},
	)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	return result, nil
}

// collectReferenceWorkflowFiles returns the tsls reference-workflow file set for one target file: all
// supported, adapter-non-ignored files under the validated target directory, with the target last.
func collectReferenceWorkflowFiles(workspaceRoot, targetRelativePath string) ([]string, error) {
	return helpers.CollectReferenceWorkflowFiles(workspaceRoot, targetRelativePath, extensions, shouldIgnoreDir)
}

// runWithReferenceWorkflowFiles opens the reference-workflow file set until the wrapped
// shared stdlsp workflow finishes.
func runWithReferenceWorkflowFiles(
	ctx context.Context,
	conn jsonrpc2.Conn,
	workspaceRoot string,
	relativePaths []string,
	withRequestDocument stdlsp.WithRequestDocumentFunc,
	run func(context.Context) error,
) error {
	absolutePaths := make([]string, 0, len(relativePaths))
	for _, relativePath := range relativePaths {
		absolutePaths = append(absolutePaths, filepath.Join(workspaceRoot, filepath.FromSlash(relativePath)))
	}

	return runWithOpenReferenceWorkflowFiles(
		ctx,
		conn,
		absolutePaths,
		withRequestDocument,
		func(callCtx context.Context) error {
		if err := warmRequestDocuments(callCtx, conn, absolutePaths); err != nil {
			return err
		}

		return run(callCtx)
		},
	)
}

// runWithOpenReferenceWorkflowFiles keeps the whole tsls workflow file set open until the wrapped call finishes.
func runWithOpenReferenceWorkflowFiles(
	ctx context.Context,
	conn jsonrpc2.Conn,
	absolutePaths []string,
	withRequestDocument stdlsp.WithRequestDocumentFunc,
	run func(context.Context) error,
) error {
	if len(absolutePaths) == 0 {
		return run(ctx)
	}

	return withRequestDocument(ctx, conn, absolutePaths[0], func(callCtx context.Context) error {
		return runWithOpenReferenceWorkflowFiles(callCtx, conn, absolutePaths[1:], withRequestDocument, run)
	})
}

// shouldIgnoreDir filters directories that are known to add noise or excessive cost to TypeScript traversal.
func shouldIgnoreDir(relativePath string) bool {
	baseName := filepath.Base(relativePath)

	if strings.HasPrefix(baseName, ".") {
		return true
	}

	switch baseName {
	case "node_modules", "dist", "build", "coverage":
		return true
	default:
		return false
	}
}
