package lspmarksman

// extensions lists the file extensions supported by Marksman for symbolic search.
//
//nolint:gochecknoglobals // ok for adapter-level constants
var extensions = []string{".md", ".markdown"}

const (
	marksmanServerName = "marksman"
	markdownLanguageID = "markdown"
)

// languageIDForExtension maps one markdown extension to the language ID expected by Marksman.
func languageIDForExtension(_ string) string {
	return markdownLanguageID
}
