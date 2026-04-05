package lsptsls

import "time"

// extensions lists the file extensions supported by the TypeScript language server.
//
//nolint:gochecknoglobals // ok for adapter-level constants
var extensions = []string{".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs"}

const (
	tslsServerName            = "typescript-language-server"
	languageIDTypeScript      = "typescript"
	languageIDTypeScriptReact = "typescriptreact"
	languageIDJavaScript      = "javascript"
	languageIDJavaScriptReact = "javascriptreact"
	tslsReferenceRetryTimeout = 2 * time.Second
	tslsReferenceRetryPoll    = 100 * time.Millisecond
)

// languageIDForExtension maps one file extension to the language ID expected by the language server.
func languageIDForExtension(ext string) string {
	switch ext {
	case ".ts", ".mts", ".cts":
		return languageIDTypeScript
	case ".tsx":
		return languageIDTypeScriptReact
	case ".js", ".mjs", ".cjs":
		return languageIDJavaScript
	case ".jsx":
		return languageIDJavaScriptReact
	default:
		return languageIDTypeScript
	}
}
