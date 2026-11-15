package lsptsls

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/cenkalti/backoff/v5"
	"go.lsp.dev/jsonrpc2"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/domain"
)

var errTSLSReferencesNotReady = errors.New("tsls references are not ready")

// FindReferencingSymbols keeps the target-directory file set open for the whole shared workflow so
// tsls can resolve cross-file references before stdlsp groups the final result.
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

	referenceWorkflowFiles, err := collectReferenceWorkflowFiles(workspaceRoot, request.File)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	conn, err := s.ensureConn(ctx, workspaceRoot)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	var result domain.FindReferencingSymbolsResult
	// failures holds every file that this request skipped, so that the retry and the final result leave out the
	// same files.
	failures := stdlsp.NewFileFailures(workspaceRoot, false)
	skipped, err := runWithReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		referenceWorkflowFiles,
		newWithRequestDocument(),
		func(callCtx context.Context, warmedPaths []string, knownSkipped []helpers.SkippedFile) error {
			if recordErr := failures.RecordSkippedFiles(callCtx, knownSkipped); recordErr != nil {
				return recordErr
			}

			resolvedRequest, targetSkipped, resolveErr := s.resolveReferenceTarget(callCtx, workspaceRoot, request)
			if resolveErr != nil {
				return resolveErr
			}
			if recordErr := failures.RecordSkippedFiles(callCtx, targetSkipped); recordErr != nil {
				return recordErr
			}

			var callErr error
			result, callErr = s.findReferencingSymbolsWithRetry(
				callCtx,
				conn,
				resolvedRequest,
				workspaceRoot,
				warmedPaths,
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

	return result, nil
}

// findReferencingSymbolsWithRetry repeats the shared reference lookup while tsls has not indexed the other open
// files yet. It adds the files that each lookup or repeated warm-up could not process to failures, and returns the
// result of the last lookup without the rows of any file in failures.
func (s *Service) findReferencingSymbolsWithRetry(
	ctx context.Context,
	conn jsonrpc2.Conn,
	request *domain.FindReferencingSymbolsRequest,
	workspaceRoot string,
	referenceWorkflowFiles []string,
	failures *stdlsp.FileFailures,
) (domain.FindReferencingSymbolsResult, error) {
	attempt := 0
	result, err := backoff.Retry(ctx, func() (domain.FindReferencingSymbolsResult, error) {
		if attempt > 0 {
			warmedPaths, warmSkipped, warmErr := warmReferenceWorkflowFiles(
				ctx,
				conn,
				workspaceRoot,
				referenceWorkflowFiles,
			)
			if warmErr != nil {
				return domain.FindReferencingSymbolsResult{}, backoff.Permanent(warmErr)
			}
			referenceWorkflowFiles = warmedPaths
			if recordErr := failures.RecordSkippedFiles(ctx, warmSkipped); recordErr != nil {
				return domain.FindReferencingSymbolsResult{}, backoff.Permanent(recordErr)
			}
		}
		attempt++

		currentResult, currentFailures, currentErr := s.FindReferencingSymbolsWithFailures(ctx, request)
		if currentErr != nil {
			return currentResult, backoff.Permanent(currentErr)
		}
		failures.Merge(currentFailures)
		// Rows of failed files are removed from the answer, so only the usable rows decide whether to retry.
		failures.CompleteReferenceResult(&currentResult)
		if shouldRetryReferenceResult(request.File, referenceWorkflowFiles, currentResult) {
			return currentResult, errTSLSReferencesNotReady
		}

		return currentResult, nil
	},
		backoff.WithBackOff(backoff.NewConstantBackOff(tslsReferenceRetryPoll)),
		backoff.WithMaxElapsedTime(tslsReferenceRetryTimeout),
	)
	if errors.Is(err, errTSLSReferencesNotReady) {
		return result, nil
	}

	return result, err
}

func shouldRetryReferenceResult(
	targetRelativePath string,
	referenceWorkflowFiles []string,
	result domain.FindReferencingSymbolsResult,
) bool {
	if !referenceWorkflowIncludesAdditionalFiles(targetRelativePath, referenceWorkflowFiles) {
		return false
	}
	if len(result.Symbols) == 0 {
		return true
	}

	normalizedTargetPath := filepath.ToSlash(filepath.Clean(targetRelativePath))
	for _, symbol := range result.Symbols {
		if filepath.ToSlash(filepath.Clean(symbol.File)) != normalizedTargetPath {
			return false
		}
	}

	return true
}

func referenceWorkflowIncludesAdditionalFiles(targetRelativePath string, referenceWorkflowFiles []string) bool {
	normalizedTargetPath := filepath.ToSlash(filepath.Clean(targetRelativePath))
	for _, relativePath := range referenceWorkflowFiles {
		if filepath.ToSlash(filepath.Clean(relativePath)) != normalizedTargetPath {
			return true
		}
	}

	return false
}

// collectReferenceWorkflowFiles returns the tsls reference-workflow file set for one target file: all
// supported, adapter-non-ignored files under the validated target directory, with the target last.
func collectReferenceWorkflowFiles(workspaceRoot, targetRelativePath string) ([]string, error) {
	return helpers.CollectReferenceWorkflowFiles(workspaceRoot, targetRelativePath, extensions, shouldIgnoreDir)
}

// runWithReferenceWorkflowFiles opens the reference-workflow file set and warms the opened files until the wrapped
// shared stdlsp workflow finishes, and returns the files that could not be opened, warmed, or closed. run receives
// the warmed files and the files that could not be opened or warmed.
func runWithReferenceWorkflowFiles(
	ctx context.Context,
	conn jsonrpc2.Conn,
	workspaceRoot string,
	relativePaths []string,
	withRequestDocument stdlsp.WithRequestDocumentFunc,
	run func(ctx context.Context, warmedPaths []string, skipped []helpers.SkippedFile) error,
) ([]helpers.SkippedFile, error) {
	var warmSkipped []helpers.SkippedFile
	openSkipped, err := helpers.RunWithReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		relativePaths,
		withRequestDocument,
		func(callCtx context.Context, openedPaths []string, skipped []helpers.SkippedFile) error {
			warmedPaths, warmFailures, warmErr := warmReferenceWorkflowFiles(callCtx, conn, workspaceRoot, openedPaths)
			if warmErr != nil {
				return warmErr
			}
			warmSkipped = warmFailures

			return run(callCtx, warmedPaths, append(skipped, warmFailures...))
		},
	)

	return append(openSkipped, warmSkipped...), err
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
