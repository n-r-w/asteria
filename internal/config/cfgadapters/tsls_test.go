package cfgadapters

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnvConfigTSLSBuildAcceptsOptionalAbsoluteFallbackPath proves that TypeScript fallback configuration remains
// optional while preserving one normalized absolute tsserver.js path when configured.
func TestEnvConfigTSLSBuildAcceptsOptionalAbsoluteFallbackPath(t *testing.T) {
	t.Parallel()

	emptyConfig, err := (EnvConfig{}).Build()
	require.NoError(t, err)
	assert.Empty(t, emptyConfig.TSLS.TSServerFallbackPath)

	fallbackPath := filepath.Join(t.TempDir(), "typescript", "lib", "tsserver.js")
	config, err := (EnvConfig{
		Gopls:        envConfigGopls{},
		TSLS:         envConfigTSLS{TSServerFallbackPath: fallbackPath},
		RustAnalyzer: envConfigRustAnalyzer{},
	}).Build()
	require.NoError(t, err)
	assert.Equal(t, filepath.Clean(fallbackPath), config.TSLS.TSServerFallbackPath)
}

// TestEnvConfigTSLSBuildRejectsRelativeFallbackPath keeps TypeScript SDK lookup independent from the Asteria
// process working directory.
func TestEnvConfigTSLSBuildRejectsRelativeFallbackPath(t *testing.T) {
	t.Parallel()

	_, err := (EnvConfig{
		Gopls: envConfigGopls{},
		TSLS: envConfigTSLS{
			TSServerFallbackPath: filepath.Join("typescript", "lib", "tsserver.js"),
		},
		RustAnalyzer: envConfigRustAnalyzer{},
	}).Build()
	require.EqualError(t, err, "ASTERIAMCP_TSLS_TSSERVER_FALLBACK_PATH must be absolute")
}
