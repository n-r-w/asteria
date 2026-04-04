package domain

import (
	"fmt"
	"strings"
)

// NormalizePublicPath keeps empty path values readable in public and shared error messages.
func NormalizePublicPath(path string) string {
	trimmedPath := strings.TrimSpace(path)
	if trimmedPath == "" {
		return "."
	}

	return trimmedPath
}

// NewInternalError hides unexpected implementation details behind one stable public message.
func NewInternalError(cause error) *SafeError {
	return NewSafeError("internal error", cause)
}

// NewUnsupportedExtensionError reports that one file extension has no symbolic search support.
func NewUnsupportedExtensionError(extension string) *SafeError {
	return NewSafeError(fmt.Sprintf("files with extension %q are not supported", extension), nil)
}

// NewPathNotFoundError reports that one public or shared path argument does not exist.
func NewPathNotFoundError(argName, path string, cause error) *SafeError {
	return NewSafeError(fmt.Sprintf("%s %q not found", argName, NormalizePublicPath(path)), cause)
}

// NewPathAccessError reports that one path could not be accessed for an unexpected reason.
func NewPathAccessError(argName, path string, cause error) *SafeError {
	return NewSafeError(fmt.Sprintf("failed to access %s %q", argName, NormalizePublicPath(path)), cause)
}

// NewPathReadError reports that one path could not be read after validation succeeded.
func NewPathReadError(argName, path string, cause error) *SafeError {
	return NewSafeError(fmt.Sprintf("failed to read %s %q", argName, NormalizePublicPath(path)), cause)
}

// NewPathPointsToDirectoryError reports that one file-only path currently resolves to a directory.
func NewPathPointsToDirectoryError(argName, path string) *SafeError {
	return NewSafeError(fmt.Sprintf("%s %q points to a directory", argName, NormalizePublicPath(path)), nil)
}

// NewPathMustPointToFileWithExtensionError reports that one file-only path lacks a file extension.
func NewPathMustPointToFileWithExtensionError(argName, path string) *SafeError {
	return NewSafeError(
		fmt.Sprintf("%s %q must point to a file with extension", argName, NormalizePublicPath(path)),
		nil,
	)
}

// NewPathMustPointToDirectoryError reports that one directory-only path currently resolves to a file.
func NewPathMustPointToDirectoryError(argName, path string) *SafeError {
	return NewSafeError(
		fmt.Sprintf("%s %q must point to a directory", argName, NormalizePublicPath(path)),
		nil,
	)
}

// NewPathMustBeAbsoluteError reports that one path argument must be absolute.
func NewPathMustBeAbsoluteError(argName, path string) *SafeError {
	return NewSafeError(
		fmt.Sprintf("%s %q must be absolute", argName, NormalizePublicPath(path)),
		nil,
	)
}

// NewPathMustBeWorkspaceRelativeError reports that one path argument must stay workspace-relative.
func NewPathMustBeWorkspaceRelativeError(argName, path string) *SafeError {
	return NewSafeError(
		fmt.Sprintf("%s %q must be workspace-relative", argName, NormalizePublicPath(path)),
		nil,
	)
}

// NewPathEscapesWorkspaceRootError reports that one path escapes the configured workspace root.
func NewPathEscapesWorkspaceRootError(argName, path string) *SafeError {
	return NewSafeError(
		fmt.Sprintf("%s %q escapes workspace root", argName, NormalizePublicPath(path)),
		nil,
	)
}
