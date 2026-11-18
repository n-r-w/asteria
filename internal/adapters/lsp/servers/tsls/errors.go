package lsptsls

// sourceFileError marks an error of reading the symbols of the source file of a re-export alias, so that a search
// attributes it to that file instead of the alias file.
type sourceFileError struct {
	// relativePath is the workspace-relative path of the source file.
	relativePath string
	err          error
}

// newSourceFileError attributes err to the source file at relativePath.
func newSourceFileError(relativePath string, err error) error {
	return &sourceFileError{relativePath: relativePath, err: err}
}

// Error returns the text of the underlying error unchanged.
func (e *sourceFileError) Error() string {
	return e.err.Error()
}

// Unwrap returns the underlying error for errors.Is/errors.As checks.
func (e *sourceFileError) Unwrap() error {
	return e.err
}
