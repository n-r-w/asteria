package helpers

// SkippedFile names one file of the reference workflow file set that could not be opened, with the error.
type SkippedFile struct {
	// RelativePath is the workspace-relative slash path of the file.
	RelativePath string
	// Err tells why the file could not be opened.
	Err error
}
