// Package helpers keeps adapter helpers shared across packages.
package helpers

import (
	"context"
	"path/filepath"

	"github.com/n-r-w/asteria/internal/domain"
	"go.lsp.dev/jsonrpc2"
)

// CollectReferenceWorkflowFiles returns one target-directory file set for a cross-file references workflow:
// all supported, adapter-non-ignored files under the validated target directory, with the target last.
func CollectReferenceWorkflowFiles(
	workspaceRoot string,
	targetRelativePath string,
	extensions []string,
	ignoreDir func(relativePath string) bool,
) ([]string, error) {
	cleanTargetRelativePath, targetAbsolutePath, err := ResolveDocumentPath(workspaceRoot, targetRelativePath)
	if err != nil {
		return nil, err
	}

	targetDirAbsolutePath := filepath.Dir(targetAbsolutePath)
	targetDirRelativePath := filepath.ToSlash(filepath.Dir(cleanTargetRelativePath))
	if targetDirRelativePath == "." {
		targetDirRelativePath = ""
	}

	referenceWorkflowFiles, err := CollectDirectoryFiles(workspaceRoot, targetDirAbsolutePath, extensions, ignoreDir)
	if err != nil {
		return nil, domain.NewPathReadError("relative path", targetDirRelativePath, err)
	}

	workflowFiles := make([]string, 0, len(referenceWorkflowFiles))
	for _, relativePath := range referenceWorkflowFiles {
		if relativePath != cleanTargetRelativePath {
			workflowFiles = append(workflowFiles, relativePath)
		}
	}

	return append(workflowFiles, cleanTargetRelativePath), nil
}

// RunWithReferenceWorkflowFiles keeps the supplied file set open until the wrapped shared references
// workflow finishes.
func RunWithReferenceWorkflowFiles(
	ctx context.Context,
	conn jsonrpc2.Conn,
	workspaceRoot string,
	relativePaths []string,
	withRequestDocument func(context.Context, jsonrpc2.Conn, string, func(context.Context) error) error,
	run func(context.Context) error,
) error {
	if len(relativePaths) == 0 {
		return run(ctx)
	}

	absolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(relativePaths[0]))

	return withRequestDocument(ctx, conn, absolutePath, func(callCtx context.Context) error {
		return RunWithReferenceWorkflowFiles(callCtx, conn, workspaceRoot, relativePaths[1:], withRequestDocument, run)
	})
}
