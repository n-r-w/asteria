//nolint:lll // ok for env vars and config fields
package cfgadapters

import "time"

// RustAnalyzerConfig holds rust-analyzer-specific settings loaded from environment variables.
type RustAnalyzerConfig struct {
	WorkspaceSymbolSearchLimit int
	StartupReadyTimeout        time.Duration
}

type envConfigRustAnalyzer struct {
	WorkspaceSymbolSearchLimit int           `env:"ASTERIAMCP_RUST_ANALYZER_WORKSPACE_SYMBOL_SEARCH_LIMIT" envDefault:"128"`
	StartupReadyTimeout        time.Duration `env:"ASTERIAMCP_RUST_ANALYZER_STARTUP_READY_TIMEOUT" envDefault:"30s"`
}

// build converts rust-analyzer-specific environment variables into runtime configuration.
//
//nolint:staticcheck,unparam // for future usage
func (e envConfigRustAnalyzer) build() (RustAnalyzerConfig, error) {
	return RustAnalyzerConfig{
		WorkspaceSymbolSearchLimit: e.WorkspaceSymbolSearchLimit,
		StartupReadyTimeout:        e.StartupReadyTimeout,
	}, nil
}
