// Package lsprustanalyzer implements the rust-analyzer standard-LSP adapter.
package lsprustanalyzer

// extensions lists the file extensions supported by rust-analyzer for symbolic search.
//
//nolint:gochecknoglobals // ok for adapter-level constants
var extensions = []string{".rs"}

const (
	// startupReadyTimeoutEnvName names the setting that limits how long startup indexing may take.
	startupReadyTimeoutEnvName       = "ASTERIAMCP_RUST_ANALYZER_STARTUP_READY_TIMEOUT"
	rustAnalyzerServerName           = "rust-analyzer"
	rustLanguageID                   = "rust"
	rustImplSymbolPrefix             = "impl "
	rustMemberPathComponentCount     = 2
	rustTargetDirName                = "target"
	rustAnalyzerCargoTargetDirName   = "cargo-target"
	experimentalServerStatusMethod   = "experimental/serverStatus"
	workspaceDiagnosticRefreshMethod = "workspace/diagnostic/refresh"
)
