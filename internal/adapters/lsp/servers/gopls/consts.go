package lspgopls

// extensions lists the file extensions supported by gopls for symbolic search.
//
//nolint:gochecknoglobals // ok for constants
var extensions = []string{goExtension}

const (
	goExtension     = ".go"
	goplsLanguageID = "go"
)
