// Package helpers keeps adapter helpers shared across packages.
package helpers

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/n-r-w/asteria/internal/domain"
)

const parentDirMarker = ".."

// ResolveWorkspaceRoot validates one absolute workspace root and normalizes symlinks.
func ResolveWorkspaceRoot(workspaceRoot string) (string, error) {
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	if trimmedWorkspaceRoot == "" {
		return "", errors.New("workspace_root is required")
	}
	if !filepath.IsAbs(trimmedWorkspaceRoot) {
		return "", domain.NewPathMustBeAbsoluteError("workspace_root", trimmedWorkspaceRoot)
	}

	cleanWorkspaceRoot := filepath.Clean(trimmedWorkspaceRoot)
	normalizedWorkspaceRoot, err := filepath.EvalSymlinks(cleanWorkspaceRoot)
	if err != nil {
		return "", sanitizeWorkspaceRootError(cleanWorkspaceRoot, err)
	}

	fileInfo, err := os.Stat(normalizedWorkspaceRoot)
	if err != nil {
		return "", sanitizeWorkspaceRootError(cleanWorkspaceRoot, err)
	}
	if !fileInfo.IsDir() {
		return "", domain.NewPathMustPointToDirectoryError("workspace_root", cleanWorkspaceRoot)
	}

	return normalizedWorkspaceRoot, nil
}

// ResolveDocumentPath normalizes one workspace-relative file path into a validated absolute file path.
func ResolveDocumentPath(workspaceRootPath, relativePath string) (cleanRelativePath, absolutePath string, err error) {
	trimmedRelativePath := strings.TrimSpace(relativePath)
	if trimmedRelativePath == "" {
		return "", "", errors.New("relative path is required")
	}

	cleanRelativePath = filepath.Clean(trimmedRelativePath)
	if cleanRelativePath == "." || filepath.IsAbs(cleanRelativePath) {
		return "", "", domain.NewSafeError(
			fmt.Sprintf("relative path %q must point to a workspace file", domain.NormalizePublicPath(relativePath)),
			nil,
		)
	}

	absolutePath = filepath.Join(workspaceRootPath, cleanRelativePath)
	relativeToRoot, err := filepath.Rel(workspaceRootPath, absolutePath)
	if err != nil {
		return "", "", domain.NewInternalError(fmt.Errorf("resolve relative path %q: %w", relativePath, err))
	}
	if relativeToRoot == parentDirMarker || strings.HasPrefix(relativeToRoot, parentDirMarker+string(os.PathSeparator)) {
		return "", "", domain.NewPathEscapesWorkspaceRootError("relative path", relativePath)
	}

	fileInfo, err := os.Stat(absolutePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", "", domain.NewPathNotFoundError("relative path", cleanRelativePath, err)
		}

		return "", "", domain.NewPathAccessError("relative path", cleanRelativePath, err)
	}
	if fileInfo.IsDir() {
		return "", "", domain.NewPathPointsToDirectoryError("relative path", cleanRelativePath)
	}

	return cleanRelativePath, absolutePath, nil
}

// CollectDirectoryFiles expands one absolute directory into sorted workspace-relative supported files.
func CollectDirectoryFiles(
	workspaceRootPath string,
	absoluteDirPath string,
	extensions []string,
	ignoreDir func(relativePath string) bool,
) ([]string, error) {
	supportedExtensions := make(map[string]struct{}, len(extensions))
	for _, extension := range extensions {
		supportedExtensions[strings.ToLower(extension)] = struct{}{}
	}

	files := make([]string, 0)
	walkErr := filepath.WalkDir(absoluteDirPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == absoluteDirPath {
				return nil
			}

			relativePath, relErr := filepath.Rel(workspaceRootPath, path)
			if relErr != nil {
				return relErr
			}
			if ignoreDir != nil && ignoreDir(relativePath) {
				return filepath.SkipDir
			}

			return nil
		}
		if _, ok := supportedExtensions[strings.ToLower(filepath.Ext(entry.Name()))]; !ok {
			return nil
		}

		relativePath, relErr := filepath.Rel(workspaceRootPath, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, relativePath)

		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}

	sort.Strings(files)

	return files, nil
}

// sanitizeWorkspaceRootError maps workspace-root filesystem failures to public-safe validation errors.
func sanitizeWorkspaceRootError(workspaceRoot string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return domain.NewPathNotFoundError("workspace_root", workspaceRoot, err)
	}

	return domain.NewPathAccessError("workspace_root", workspaceRoot, err)
}
