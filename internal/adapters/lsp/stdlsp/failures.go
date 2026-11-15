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

// FileFailures collects files that one directory or workspace search could not process, with the reasons.
type FileFailures struct {
	// workspaceRoot is removed from reasons so that they name files relative to the workspace.
	workspaceRoot string
	// singleFile reports that the search scope is one file, so its error belongs to the whole search.
	singleFile bool
	// failures keeps the first reason of each failed file in the order of failure.
	failures []fileFailure
	// failedPaths indexes failures by workspace-relative path.
	failedPaths map[string]struct{}
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
		workspaceRoot: workspaceRoot,
		singleFile:    singleFile,
		failures:      nil,
		failedPaths:   make(map[string]struct{}),
	}
}

// Record attributes err to one file and returns nil, or returns err when err must end the whole search:
// the search scope is this one file, the request context ended, or no language server session is available.
func (f *FileFailures) Record(ctx context.Context, relativePath string, err error) error {
	if err == nil {
		return nil
	}
	if f.singleFile || helpers.IsRequestError(ctx, err) {
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

// RecordSkippedFiles records the files that a reference workflow skipped, or returns the first error that must end
// the whole search. A search of one file ignores them, because no result row comes from them.
func (f *FileFailures) RecordSkippedFiles(ctx context.Context, skipped []helpers.SkippedFile) error {
	if f.singleFile {
		return nil
	}
	for _, skippedFile := range skipped {
		if err := f.Record(ctx, skippedFile.RelativePath, skippedFile.Err); err != nil {
			return err
		}
	}

	return nil
}

// Merge adds the failed files of other that this collector does not hold yet, with their reasons.
func (f *FileFailures) Merge(other *FileFailures) {
	for _, failure := range other.failures {
		if f.contains(failure.RelativePath) {
			continue
		}
		f.failedPaths[failure.RelativePath] = struct{}{}
		f.failures = append(f.failures, failure)
	}
}

// CompleteSymbolResult removes the symbols of failed files from a symbol search result and adds the warning
// that names these files.
func (f *FileFailures) CompleteSymbolResult(result *domain.FindSymbolResult) {
	result.Symbols = slices.DeleteFunc(result.Symbols, func(symbol domain.FoundSymbol) bool {
		return f.contains(symbol.File)
	})
	result.Warnings = f.warnings(symbolSearchFailureAction)
}

// CompleteReferenceResult removes the references of failed files from a reference search result and adds the
// warning that names these files.
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

// warnings builds one line with the failed file count, the first files with their reasons, and the count of the rest.
func (f *FileFailures) warnings(action string) []string {
	if len(f.failures) == 0 {
		return nil
	}

	extensions := make([]string, 0, 1)
	for _, failure := range f.failures {
		extension := strings.ToLower(filepath.Ext(failure.RelativePath))
		if extension != "" && !slices.Contains(extensions, extension) {
			extensions = append(extensions, extension)
		}
	}
	slices.Sort(extensions)

	var line strings.Builder
	if len(extensions) > 0 {
		line.WriteString(strings.Join(extensions, ", ") + ": ")
	}
	if len(f.failures) == 1 {
		fmt.Fprintf(&line, "1 file is not %s: ", action)
	} else {
		fmt.Fprintf(&line, "%d files are not %s: ", len(f.failures), action)
	}

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

	return []string{line.String()}
}
