// Package lsprustanalyzer implements the rust-analyzer standard-LSP adapter.
package lsprustanalyzer

// extensions lists the file extensions supported by rust-analyzer for symbolic search.
//
//nolint:gochecknoglobals // ok for adapter-level constants
var extensions = []string{".rs"}

const (
	rustAnalyzerServerName           = "rust-analyzer"
	rustLanguageID                   = "rust"
	rustImplSymbolPrefix             = "impl "
	rustMemberPathComponentCount     = 2
	rustTargetDirName                = "target"
	rustAnalyzerCargoTargetDirName   = "cargo-target"
	experimentalServerStatusMethod   = "experimental/serverStatus"
	workspaceDiagnosticRefreshMethod = "workspace/diagnostic/refresh"
)
