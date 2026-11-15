// Package helpers keeps adapter helpers shared across packages.
package helpers

import (
	"context"
	"path/filepath"
	"slices"

	"go.lsp.dev/jsonrpc2"

	"github.com/n-r-w/asteria/internal/domain"
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
// workflow finishes. A file that cannot be opened is skipped and returned, unless it is the target, which is the last
// file, or its error ends the whole request. A file that cannot be closed after the workflow finished is skipped and
// returned also after the request deadline, unless its error ends the whole request.
// run receives the files that are open and the files that could not be opened.
func RunWithReferenceWorkflowFiles(
	ctx context.Context,
	conn jsonrpc2.Conn,
	workspaceRoot string,
	relativePaths []string,
	withRequestDocument func(context.Context, jsonrpc2.Conn, string, func(context.Context) error) error,
	run func(ctx context.Context, openedPaths []string, skipped []SkippedFile) error,
) ([]SkippedFile, error) {
	var skipped []SkippedFile
	err := openReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		relativePaths,
		make([]string, 0, len(relativePaths)),
		&skipped,
		withRequestDocument,
		run,
	)

	return skipped, err
}

// openReferenceWorkflowFiles opens the remaining files one inside another and runs the workflow inside the last one.
func openReferenceWorkflowFiles(
	ctx context.Context,
	conn jsonrpc2.Conn,
	workspaceRoot string,
	remainingPaths []string,
	openedPaths []string,
	skipped *[]SkippedFile,
	withRequestDocument func(context.Context, jsonrpc2.Conn, string, func(context.Context) error) error,
	run func(ctx context.Context, openedPaths []string, skipped []SkippedFile) error,
) error {
	if len(remainingPaths) == 0 {
		return run(ctx, openedPaths, slices.Clone(*skipped))
	}

	relativePath := remainingPaths[0]
	absolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(relativePath))
	// opened separates an error of opening this file from an error of the work that runs inside it, and workflowErr
	// separates an error of that work from an error of closing this file after the work finished.
	opened := false
	var workflowErr error
	err := withRequestDocument(ctx, conn, absolutePath, func(callCtx context.Context) error {
		opened = true
		workflowErr = openReferenceWorkflowFiles(
			callCtx,
			conn,
			workspaceRoot,
			remainingPaths[1:],
			append(openedPaths, relativePath),
			skipped,
			withRequestDocument,
			run,
		)

		return workflowErr
	})
	if err == nil {
		return nil
	}
	if opened && workflowErr == nil {
		if IsSkippedFileRequestError(ctx, err) {
			return err
		}
		// The workflow already finished, and only closing this file failed: it is reported without a new run.
		*skipped = append(*skipped, SkippedFile{RelativePath: relativePath, Err: err})

		return nil
	}
	if opened || IsRequestError(ctx, err) {
		return err
	}
	if len(remainingPaths) == 1 {
		return err
	}

	*skipped = append(*skipped, SkippedFile{RelativePath: relativePath, Err: err})

	return openReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		remainingPaths[1:],
		openedPaths,
		skipped,
		withRequestDocument,
		run,
	)
}
