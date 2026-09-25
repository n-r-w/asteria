package cfgadapters

import (
	"errors"
	"path/filepath"
	"strings"
)

// TSLSConfig holds typescript-language-server-specific settings loaded from environment variables.
type TSLSConfig struct {
	TSServerFallbackPath string
}

type envConfigTSLS struct {
	TSServerFallbackPath string `env:"ASTERIAMCP_TSLS_TSSERVER_FALLBACK_PATH"`
}

// build converts TypeScript language-server environment variables into runtime configuration.
func (e envConfigTSLS) build() (TSLSConfig, error) {
	fallbackPath := strings.TrimSpace(e.TSServerFallbackPath)
	if fallbackPath == "" {
		return TSLSConfig{}, nil
	}
	if !filepath.IsAbs(fallbackPath) {
		return TSLSConfig{}, errors.New("ASTERIAMCP_TSLS_TSSERVER_FALLBACK_PATH must be absolute")
	}

	return TSLSConfig{TSServerFallbackPath: filepath.Clean(fallbackPath)}, nil
}
