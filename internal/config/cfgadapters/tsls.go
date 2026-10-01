package cfgadapters

import (
	"errors"
	"path/filepath"
	"strings"
)

// TSLSConfig holds typescript-language-server-specific settings loaded from environment variables.
type TSLSConfig struct {
	TSServerFallbackPath string
	MaxTSServerMemory    int
}

type envConfigTSLS struct {
	TSServerFallbackPath string `env:"ASTERIAMCP_TSLS_TSSERVER_FALLBACK_PATH"`
	MaxTSServerMemory    *int   `env:"ASTERIAMCP_TSLS_MAX_TSSERVER_MEMORY"`
}

// build converts TypeScript language-server environment variables into runtime configuration.
func (e envConfigTSLS) build() (TSLSConfig, error) {
	maxMemory := DefaultMaxTSServerMemory
	if e.MaxTSServerMemory != nil {
		maxMemory = *e.MaxTSServerMemory
	}
	if maxMemory <= 0 {
		return TSLSConfig{}, errors.New("ASTERIAMCP_TSLS_MAX_TSSERVER_MEMORY must be positive")
	}

	fallbackPath := strings.TrimSpace(e.TSServerFallbackPath)
	if fallbackPath != "" {
		if !filepath.IsAbs(fallbackPath) {
			return TSLSConfig{}, errors.New("ASTERIAMCP_TSLS_TSSERVER_FALLBACK_PATH must be absolute")
		}
		fallbackPath = filepath.Clean(fallbackPath)
	}

	return TSLSConfig{TSServerFallbackPath: fallbackPath, MaxTSServerMemory: maxMemory}, nil
}
