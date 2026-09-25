package lsptsls

import "time"

// extensions lists the file extensions supported by the TypeScript language server.
//
//nolint:gochecknoglobals // ok for adapter-level constants
var extensions = []string{
	tsExtension,
	tsxExtension,
	jsExtension,
	jsxExtension,
	mtsExtension,
	ctsExtension,
	mjsExtension,
	cjsExtension,
}

const (
	tsExtension               = ".ts"
	tsxExtension              = ".tsx"
	jsExtension               = ".js"
	jsxExtension              = ".jsx"
	mtsExtension              = ".mts"
	ctsExtension              = ".cts"
	mjsExtension              = ".mjs"
	cjsExtension              = ".cjs"
	tslsServerName            = "typescript-language-server"
	tsserverOptionsKey        = "tsserver"
	tsserverFallbackPathKey   = "fallbackPath"
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
	case tsExtension, mtsExtension, ctsExtension:
		return languageIDTypeScript
	case tsxExtension:
		return languageIDTypeScriptReact
	case jsExtension, mjsExtension, cjsExtension:
		return languageIDJavaScript
	case jsxExtension:
		return languageIDJavaScriptReact
	default:
		return languageIDTypeScript
	}
}
