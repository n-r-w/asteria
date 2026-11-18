package stdlsp

const (
	// parentDirMarker centralizes the workspace-escape guard for relative paths.
	parentDirMarker = ".."
	// namePathDiscriminatorSeparator separates one symbol name from the position-based duplicate discriminator.
	namePathDiscriminatorSeparator = "@"
	// namePathDiscriminatorValueSeparator separates the line and character inside one duplicate discriminator.
	namePathDiscriminatorValueSeparator = ":"
)

const (
	// maxNamedFailedFiles limits how many failed files one warning names with their reasons.
	maxNamedFailedFiles = 3
	// symbolSearchFailureAction tells what happened to files that find_symbol could not process.
	symbolSearchFailureAction = "searched"
	// referenceSearchFailureAction tells what happened to files that find_referencing_symbols could not process.
	referenceSearchFailureAction = "processed"
)
