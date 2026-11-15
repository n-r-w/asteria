package stdlsp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
)

// FileFailures collects, for one symbol or reference search, the files that the search could not process, with the
// reasons, and the files that are not searched because the search or a step after it stopped at the request deadline.
type FileFailures struct {
	// workspaceRoot is removed from reasons so that they name files relative to the workspace.
	workspaceRoot string
	// singleFile reports that the search scope is one file, so its error belongs to the whole search.
	singleFile bool
	// failures keeps the first reason of each failed file in the order of failure.
	failures []fileFailure
	// failedPaths indexes failures by workspace-relative path.
	failedPaths map[string]struct{}
	// notSearchedPaths holds the files that are not searched because the request deadline passed: their
	// workspace-relative paths, or file system paths for files outside the workspace.
	notSearchedPaths map[string]struct{}
	// stoppedByTimeout reports that the search or a step after it stopped because the request deadline passed.
	stoppedByTimeout bool
	// scopeFiles holds the files of the search scope, which count as not searched when a step after the finished
	// search is left out because the request deadline passed.
	scopeFiles []string
}

// fileFailure keeps one file that a search could not process.
type fileFailure struct {
	// RelativePath is the workspace-relative path of the file.
	RelativePath string
	// Reason is the error text without the workspace root.
	Reason string
}

// NewFileFailures creates an empty collector for one search over the given workspace scope; singleFile reports
// that the scope is one file, whose error ends the search.
func NewFileFailures(workspaceRoot string, singleFile bool) *FileFailures {
	return &FileFailures{
		workspaceRoot:    workspaceRoot,
		singleFile:       singleFile,
		failures:         nil,
		failedPaths:      make(map[string]struct{}),
		notSearchedPaths: make(map[string]struct{}),
		stoppedByTimeout: false,
		scopeFiles:       nil,
	}
}

// Record attributes err to one file and returns nil, or returns err when err must end the whole search:
// the search scope is this one file, the request context ended, or no language server session is available.
func (f *FileFailures) Record(ctx context.Context, relativePath string, err error) error {
	return f.record(relativePath, err, helpers.IsRequestError(ctx, err))
}

// record attributes err to one file and returns nil, or returns err when endsRequest is set or the search scope is
// this one file.
func (f *FileFailures) record(relativePath string, err error, endsRequest bool) error {
	if err == nil {
		return nil
	}
	if f.singleFile || endsRequest {
		return err
	}
	if f.contains(relativePath) {
		return nil
	}

	failedPath := normalizeFailurePath(relativePath)
	f.failedPaths[failedPath] = struct{}{}
	f.failures = append(f.failures, fileFailure{RelativePath: failedPath, Reason: f.reason(relativePath, err)})

	return nil
}

// StopByTimeout reports whether a search whose scope is not one file must stop because the request deadline passed,
// and in that case records the notSearched files as not searched.
func (f *FileFailures) StopByTimeout(ctx context.Context, notSearched []string) bool {
	if f.singleFile || !domain.IsStoppedByTimeout(ctx) {
		return false
	}
	f.stoppedByTimeout = true
	for _, relativePath := range notSearched {
		f.notSearchedPaths[normalizeFailurePath(relativePath)] = struct{}{}
	}

	return true
}

// StoppedByTimeout reports whether the search or a step after it stopped because the request deadline passed.
func (f *FileFailures) StoppedByTimeout() bool {
	return f.stoppedByTimeout
}

// StopFollowUpByTimeout reports whether an adapter must leave out a step after a search whose scope is not one file
// because the request deadline passed, and in that case records the files of the search scope as not searched when
// the search itself finished.
func (f *FileFailures) StopFollowUpByTimeout(ctx context.Context) bool {
	return f.StoppedByTimeout() || f.StopByTimeout(ctx, f.scopeFiles)
}

// reason returns the shortest error text that still explains why the file failed: the file system operation
// and its error when they concern this file, otherwise the error text without the workspace root and without the
// quoted file names that only repeat the file.
func (f *FileFailures) reason(relativePath string, err error) string {
	if pathErr, ok := errors.AsType[*fs.PathError](err); ok &&
		filepath.Clean(pathErr.Path) == filepath.Join(f.workspaceRoot, relativePath) {
		return pathErr.Op + ": " + pathErr.Err.Error()
	}

	reason := strings.ReplaceAll(err.Error(), f.workspaceRoot+string(filepath.Separator), "")
	// The warning already names the file, so the quoted file names that wrappers add only repeat it.
	for _, quotedPath := range []string{
		strconv.Quote(relativePath),
		strconv.Quote(filepath.ToSlash(relativePath)),
	} {
		reason = strings.ReplaceAll(reason, " for "+quotedPath, "")
		reason = strings.ReplaceAll(reason, " "+quotedPath, "")
	}

	return reason
}

// RecordSkippedFiles records the files that a reference workflow skipped, also after the request deadline, or returns
// the first error that must end the whole search. A search of one file ignores them, because no result row comes from
// them.
func (f *FileFailures) RecordSkippedFiles(ctx context.Context, skipped []helpers.SkippedFile) error {
	if f.singleFile {
		return nil
	}
	for _, skippedFile := range skipped {
		endsRequest := helpers.IsSkippedFileRequestError(ctx, skippedFile.Err)
		if err := f.record(skippedFile.RelativePath, skippedFile.Err, endsRequest); err != nil {
			return err
		}
	}

	return nil
}

// Merge adds the failed files of other that this collector does not hold yet, with their reasons, and the files
// that other did not search because the request deadline passed.
func (f *FileFailures) Merge(other *FileFailures) {
	for _, failure := range other.failures {
		if f.contains(failure.RelativePath) {
			continue
		}
		f.failedPaths[failure.RelativePath] = struct{}{}
		f.failures = append(f.failures, failure)
	}
	for relativePath := range other.notSearchedPaths {
		f.notSearchedPaths[relativePath] = struct{}{}
	}
	f.stoppedByTimeout = f.stoppedByTimeout || other.stoppedByTimeout
}

// CompleteSymbolResult removes the symbols of failed files from a symbol search result and adds the warnings that
// name these files and count the files not searched because of the request deadline.
func (f *FileFailures) CompleteSymbolResult(result *domain.FindSymbolResult) {
	result.Symbols = slices.DeleteFunc(result.Symbols, func(symbol domain.FoundSymbol) bool {
		return f.contains(symbol.File)
	})
	result.Warnings = f.warnings(symbolSearchFailureAction)
}

// CompleteReferenceResult removes the references of failed files from a reference search result and adds the
// warnings that name these files and count the files not processed because of the request deadline.
func (f *FileFailures) CompleteReferenceResult(result *domain.FindReferencingSymbolsResult) {
	result.Symbols = slices.DeleteFunc(result.Symbols, func(symbol domain.ReferencingSymbol) bool {
		return f.contains(symbol.File)
	})
	result.Warnings = f.warnings(referenceSearchFailureAction)
}

// contains reports whether the file already failed in this search.
func (f *FileFailures) contains(relativePath string) bool {
	_, ok := f.failedPaths[normalizeFailurePath(relativePath)]

	return ok
}

// normalizeFailurePath gives one form to the paths that different producers write with '/' or '\', so that
// one file is counted and named once.
func normalizeFailurePath(relativePath string) string {
	return strings.ReplaceAll(relativePath, `\`, "/")
}

// warnings builds one line with the failed files, the first of them with their reasons and the count of the rest,
// and, after a stop by the request deadline, one timeout line that counts the files that are not searched and did not
// fail, without a count when each of them failed.
func (f *FileFailures) warnings(action string) []string {
	var warnings []string
	if len(f.failures) > 0 {
		failedPaths := make([]string, 0, len(f.failures))
		for _, failure := range f.failures {
			failedPaths = append(failedPaths, failure.RelativePath)
		}

		var line strings.Builder
		line.WriteString(formatSkippedFilesPrefix(failedPaths, action))
		namedFailures := f.failures[:min(len(f.failures), maxNamedFailedFiles)]
		for index, failure := range namedFailures {
			if index > 0 {
				line.WriteString("; ")
			}
			line.WriteString(failure.RelativePath + ": " + failure.Reason)
		}
		if rest := len(f.failures) - len(namedFailures); rest > 0 {
			fmt.Fprintf(&line, "; and %d more", rest)
		}
		warnings = append(warnings, line.String())
	}

	if !f.stoppedByTimeout {
		return warnings
	}
	// A failed file is already reported in the failed-files line, so the timeout line does not count it again; when each
	// file left after the stop also failed, the line keeps the extensions of these files and the timeout reason.
	stopPaths := make([]string, 0, len(f.notSearchedPaths))
	notSearchedPaths := make([]string, 0, len(f.notSearchedPaths))
	for relativePath := range f.notSearchedPaths {
		stopPaths = append(stopPaths, relativePath)
		if !f.contains(relativePath) {
			notSearchedPaths = append(notSearchedPaths, relativePath)
		}
	}
	if len(notSearchedPaths) > 0 {
		return append(warnings, formatSkippedFilesPrefix(notSearchedPaths, action)+domain.SearchStoppedByTimeout)
	}

	return append(warnings, formatExtensionsPrefix(stopPaths)+domain.SearchStoppedByTimeout)
}

// formatSkippedFilesPrefix starts a warning line about the given files with their sorted extensions and their count.
func formatSkippedFilesPrefix(relativePaths []string, action string) string {
	prefix := formatExtensionsPrefix(relativePaths)
	if len(relativePaths) == 1 {
		return prefix + fmt.Sprintf("1 file is not %s: ", action)
	}

	return prefix + fmt.Sprintf("%d files are not %s: ", len(relativePaths), action)
}

// formatExtensionsPrefix starts a warning line with the sorted extensions of the given files, or returns an empty
// string when the files have no extensions.
func formatExtensionsPrefix(relativePaths []string) string {
	extensions := make([]string, 0, 1)
	for _, relativePath := range relativePaths {
		extension := strings.ToLower(filepath.Ext(relativePath))
		if extension != "" && !slices.Contains(extensions, extension) {
			extensions = append(extensions, extension)
		}
	}
	if len(extensions) == 0 {
		return ""
	}
	slices.Sort(extensions)

	return strings.Join(extensions, ", ") + ": "
}
