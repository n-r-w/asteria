package stdlsp

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
)

// resolveSearchScope normalizes an optional file-or-directory scope into one safe workspace path.
func resolveSearchScope(workspaceRootPath, relativePath string) (searchScope, error) {
	trimmedRelativePath := strings.TrimSpace(relativePath)
	if trimmedRelativePath == "" {
		return searchScope{RelativePath: "", AbsolutePath: workspaceRootPath, IsDir: true}, nil
	}

	cleanRelativePath := filepath.Clean(trimmedRelativePath)
	if filepath.IsAbs(cleanRelativePath) {
		return searchScope{}, domain.NewPathMustBeWorkspaceRelativeError("relative path", relativePath)
	}
	if cleanRelativePath == "." {
		return searchScope{RelativePath: "", AbsolutePath: workspaceRootPath, IsDir: true}, nil
	}

	absolutePath := filepath.Join(workspaceRootPath, cleanRelativePath)
	relativeToRoot, err := filepath.Rel(workspaceRootPath, absolutePath)
	if err != nil {
		return searchScope{}, domain.NewInternalError(fmt.Errorf("resolve search scope %q: %w", relativePath, err))
	}
	if relativeToRoot == parentDirMarker || strings.HasPrefix(relativeToRoot, parentDirMarker+string(os.PathSeparator)) {
		return searchScope{}, domain.NewPathEscapesWorkspaceRootError("relative path", relativePath)
	}

	fileInfo, err := os.Stat(absolutePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return searchScope{}, domain.NewPathNotFoundError("relative path", cleanRelativePath, err)
		}

		return searchScope{}, domain.NewPathAccessError("relative path", cleanRelativePath, err)
	}

	return searchScope{
		RelativePath: cleanRelativePath,
		AbsolutePath: absolutePath,
		IsDir:        fileInfo.IsDir(),
	}, nil
}

// collectScopeFiles expands one search scope into all supported workspace files that an adapter should inspect.
func collectScopeFiles(
	workspaceRootPath string,
	scope searchScope,
	extensions []string,
	ignoreDir IgnoreDirFunc,
) ([]string, error) {
	supportedExtensions := make(map[string]struct{}, len(extensions))
	for _, extension := range extensions {
		supportedExtensions[strings.ToLower(extension)] = struct{}{}
	}

	if !scope.IsDir {
		if _, ok := supportedExtensions[strings.ToLower(filepath.Ext(scope.RelativePath))]; !ok {
			return nil, nil
		}

		return []string{scope.RelativePath}, nil
	}

	files, walkErr := helpers.CollectDirectoryFiles(workspaceRootPath, scope.AbsolutePath, extensions, ignoreDir)
	if walkErr != nil {
		return nil, domain.NewPathReadError("relative path", scope.RelativePath, walkErr)
	}

	return files, nil
}
