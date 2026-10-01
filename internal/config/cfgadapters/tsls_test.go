package cfgadapters

import (
	"path/filepath"
	"testing"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
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
		TSLS:         envConfigTSLS{TSServerFallbackPath: fallbackPath, MaxTSServerMemory: nil},
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
			MaxTSServerMemory:    nil,
		},
		RustAnalyzer: envConfigRustAnalyzer{},
	}).Build()
	require.EqualError(t, err, "ASTERIAMCP_TSLS_TSSERVER_FALLBACK_PATH must be absolute")
}

type tslsMemorySuite struct {
	suite.Suite
}

func TestTSLSMemorySuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(tslsMemorySuite))
}

// TestEnvironmentMapping covers the omitted default, explicit positive override, and
// invalid nonpositive limits. Environment parsing uses an isolated map, not process env.
func (s *tslsMemorySuite) TestEnvironmentMapping() {
	for _, tc := range []struct {
		name      string
		value     string
		want      int
		wantError bool
	}{
		{name: "default", value: "", want: 2048, wantError: false},
		{name: "override", value: "3072", want: 3072, wantError: false},
		{name: "zero", value: "0", want: 0, wantError: true},
		{name: "negative", value: "-1", want: 0, wantError: true},
	} {
		s.Run(tc.name, func() {
			values := map[string]string{}
			if tc.value != "" {
				values["ASTERIAMCP_TSLS_MAX_TSSERVER_MEMORY"] = tc.value
			}
			var ec EnvConfig
			var options env.Options
			options.Environment = values
			s.Require().NoError(env.ParseWithOptions(&ec, options))
			cfg, err := ec.Build()
			if tc.wantError {
				s.Require().EqualError(err, "ASTERIAMCP_TSLS_MAX_TSSERVER_MEMORY must be positive")
				return
			}
			s.Require().NoError(err)
			s.Equal(tc.want, cfg.TSLS.MaxTSServerMemory)
		})
	}
}
