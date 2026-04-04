package lspbasedpyright

// extensions lists the file extensions supported by basedpyright for symbolic search.
//
//nolint:gochecknoglobals // ok for adapter-level constants
var extensions = []string{".py", ".pyi"}

const (
	basedpyrightServerName    = "basedpyright-langserver"
	basedpyrightConfigSection = "basedpyright.analysis"
	pythonLanguageID          = "python"
)
