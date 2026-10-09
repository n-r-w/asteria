package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/stretchr/testify/suite"
)

type configSuite struct {
	suite.Suite
}

func TestConfigSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(configSuite))
}

// TestSessionIdleTimeout verifies environment parsing and mapping to application config.
// Omitted input uses ten minutes; an explicit positive duration replaces it.
func (s *configSuite) TestSessionIdleTimeout() {
	for _, tc := range []struct {
		name  string
		value string
		want  time.Duration
	}{
		{name: "default", value: "", want: 10 * time.Minute},
		{name: "override", value: "25s", want: 25 * time.Second},
	} {
		s.Run(tc.name, func() {
			ec := s.parseEnvironment(tc.value)
			cfg, err := buildConfig(ec)
			s.Require().NoError(err)
			s.Equal(tc.want, cfg.LSPSessionIdleTimeout)
		})
	}
}

// TestSessionIdleTimeoutRejectsNonpositiveDuration checks invalid external configuration
// before adapters are built. Neither case depends on a language server.
func (s *configSuite) TestSessionIdleTimeoutRejectsNonpositiveDuration() {
	for _, value := range []string{"0s", "-1s"} {
		s.Run(value, func() {
			_, err := buildConfig(s.parseEnvironment(value))
			s.Require().EqualError(err, "ASTERIAMCP_LSP_SESSION_IDLE_TIMEOUT must be positive")
		})
	}
}

// TestToolDescriptionsCarrySharedRules checks that each tool description ends with rules that apply to all tools,
// also when environment replaces tool-specific part of description.
func (s *configSuite) TestToolDescriptionsCarrySharedRules() {
	for _, override := range []string{"", "custom description"} {
		s.Run("override="+override, func() {
			ec := s.parseEnvironment("")
			ec.GetSymbolsOverviewDesc, ec.FindSymbolDesc, ec.FindReferencesDesc = override, override, override
			cfg, err := buildConfig(ec)
			s.Require().NoError(err)
			for _, description := range []string{cfg.GetSymbolsOverviewDesc, cfg.FindSymbolDesc, cfg.FindReferencesDesc} {
				s.Contains(description, "1-based")
				s.Contains(description, "Languages:")
				if override != "" {
					s.Contains(description, override)
				}
			}
		})
	}
}

func (s *configSuite) parseEnvironment(idleTimeout string) *envConfig {
	s.T().Helper()
	values := map[string]string{
		"ASTERIAMCP_CACHE_ROOT": s.T().TempDir(),
		"ASTERIAMCP_LOG_FILE":   filepath.Join(s.T().TempDir(), "asteria.log"),
	}
	if idleTimeout != "" {
		values["ASTERIAMCP_LSP_SESSION_IDLE_TIMEOUT"] = idleTimeout
	}
	var ec envConfig
	var options env.Options
	options.Environment = values
	s.Require().NoError(env.ParseWithOptions(&ec, options))
	return &ec
}
