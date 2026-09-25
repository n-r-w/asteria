// Package cfgadapters loads adapter-specific application configuration from environment variables.
package cfgadapters

// Config groups adapter-specific application configuration.
type Config struct {
	Gopls        GoplsConfig
	TSLS         TSLSConfig
	RustAnalyzer RustAnalyzerConfig
}

// EnvConfig groups adapter-specific environment variable definitions.
type EnvConfig struct {
	Gopls        envConfigGopls
	TSLS         envConfigTSLS
	RustAnalyzer envConfigRustAnalyzer
}

// Build converts parsed adapter-specific environment variables into runtime configuration.
func (e EnvConfig) Build() (Config, error) {
	goplsConfig, err := e.Gopls.build()
	if err != nil {
		return Config{}, err
	}

	tslsConfig, err := e.TSLS.build()
	if err != nil {
		return Config{}, err
	}

	rustAnalyzerConfig, err := e.RustAnalyzer.build()
	if err != nil {
		return Config{}, err
	}

	return Config{
		Gopls:        goplsConfig,
		TSLS:         tslsConfig,
		RustAnalyzer: rustAnalyzerConfig,
	}, nil
}
