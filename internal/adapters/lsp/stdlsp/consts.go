package stdlsp

const (
	// parentDirMarker centralizes the workspace-escape guard for relative paths.
	parentDirMarker = ".."
	// namePathDiscriminatorSeparator separates one symbol name from the position-based duplicate discriminator.
	namePathDiscriminatorSeparator = "@"
	// namePathDiscriminatorValueSeparator separates the line and character inside one duplicate discriminator.
	namePathDiscriminatorValueSeparator = ":"
)
