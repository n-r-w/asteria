package domain

import (
	"fmt"
	"strings"
)

// NormalizeMessagePath keeps empty path values readable in error messages.
func NormalizeMessagePath(path string) string {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return "."
	}

	return trimmedPath
}

// NewInternalError adds stable internal-error context while preserving the diagnostic cause.
func NewInternalError(cause error) *ToolError {
	return NewToolError("internal error", cause)
}

// NewUnsupportedExtensionError reports that one file extension has no symbolic search support.
func NewUnsupportedExtensionError(extension string) *ToolError {
	return NewToolError(fmt.Sprintf("files with extension %q are not supported", extension), nil)
}

// NewPathError names the path argument whose filesystem access failed.
func NewPathError(argName, path string, cause error) *ToolError {
	return NewToolError(fmt.Sprintf("%s %q", argName, NormalizeMessagePath(path)), cause)
}

// NewPathReadError reports that one path could not be read after validation succeeded.
func NewPathReadError(argName, path string, cause error) *ToolError {
	return NewToolError(fmt.Sprintf("failed to read %s %q", argName, NormalizeMessagePath(path)), cause)
}

// NewPathPointsToDirectoryError reports that one file-only path currently resolves to a directory.
func NewPathPointsToDirectoryError(argName, path string) *ToolError {
	return NewToolError(fmt.Sprintf("%s %q points to a directory", argName, NormalizeMessagePath(path)), nil)
}

// NewPathMustPointToFileWithExtensionError reports that one file-only path lacks a file extension.
func NewPathMustPointToFileWithExtensionError(argName, path string) *ToolError {
	return NewToolError(
		fmt.Sprintf("%s %q must point to a file with extension", argName, NormalizeMessagePath(path)),
		nil,
	)
}

// NewPathMustPointToDirectoryError reports that one directory-only path currently resolves to a file.
func NewPathMustPointToDirectoryError(argName, path string) *ToolError {
	return NewToolError(
		fmt.Sprintf("%s %q must point to a directory", argName, NormalizeMessagePath(path)),
		nil,
	)
}

// NewPathMustBeAbsoluteError reports that one path argument must be absolute.
func NewPathMustBeAbsoluteError(argName, path string) *ToolError {
	return NewToolError(
		fmt.Sprintf("%s %q must be absolute", argName, NormalizeMessagePath(path)),
		nil,
	)
}

// NewPathMustBeWorkspaceRelativeError reports that one path argument must stay workspace-relative.
func NewPathMustBeWorkspaceRelativeError(argName, path string) *ToolError {
	return NewToolError(
		fmt.Sprintf("%s %q must be workspace-relative", argName, NormalizeMessagePath(path)),
		nil,
	)
}

// NewPathEscapesWorkspaceRootError reports that one path escapes the configured workspace root.
func NewPathEscapesWorkspaceRootError(argName, path string) *ToolError {
	return NewToolError(
		fmt.Sprintf("%s %q escapes workspace root", argName, NormalizeMessagePath(path)),
		nil,
	)
}
