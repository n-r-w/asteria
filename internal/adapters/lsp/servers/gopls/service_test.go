package lspgopls

import (
	"testing"

	"github.com/stretchr/testify/suite"
	"go.lsp.dev/protocol"

	"github.com/n-r-w/asteria/internal/config/cfgadapters"
)

type configurationSuite struct {
	suite.Suite
}

func TestConfigurationSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(configurationSuite))
}

// TestDiagnosticSettingsPreserveBuildConfiguration verifies configuration replies
// always include the lighter diagnostic policy and retain caller build flags and env.
// An unrelated configuration section receives no gopls settings.
func (s *configurationSuite) TestDiagnosticSettingsPreserveBuildConfiguration() {
	for _, configured := range []bool{false, true} {
		name := "default"
		if configured {
			name = "build_configuration"
		}
		s.Run(name, func() {
			config := cfgadapters.GoplsConfig{BuildFlags: nil, Env: nil}
			expected := map[string]any{"diagnosticsTrigger": "Save", "staticcheck": false}
			if configured {
				config.BuildFlags = []string{"-tags=featurex"}
				config.Env = map[string]string{"GOFLAGS": "-mod=readonly"}
				expected["buildFlags"] = config.BuildFlags
				expected["env"] = config.Env
			}
			reply := buildReplyConfiguration(config)
			s.Require().NotNil(reply)
			result, err := reply("workspace", protocol.ConfigurationParams{
				Items: []protocol.ConfigurationItem{
					{Section: "gopls", ScopeURI: ""},
					{Section: "unrelated", ScopeURI: ""},
				},
			})
			s.Require().NoError(err)
			s.Equal([]any{expected, nil}, result)
		})
	}
}
