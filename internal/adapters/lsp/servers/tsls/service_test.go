package lsptsls

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/n-r-w/asteria/internal/config/cfgadapters"
)

type initializeSuite struct {
	suite.Suite
}

func TestInitializeSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(initializeSuite))
}

// TestMemoryAndFallback verifies the configured memory value and single-server mode
// reach initialization with and without an SDK fallback. Workspace fields are preserved.
func (s *initializeSuite) TestMemoryAndFallback() {
	for _, withFallback := range []bool{false, true} {
		name := "workspace_sdk"
		if withFallback {
			name = "fallback_sdk"
		}
		s.Run(name, func() {
			workspaceRoot := s.T().TempDir()
			workspaceFolders := []protocol.WorkspaceFolder{{
				URI: string(uri.File(workspaceRoot)), Name: "workspace",
			}}
			//nolint:exhaustruct_v5 // The test exercises workspace and initialization options only.
			params := &protocol.InitializeParams{WorkspaceFolders: workspaceFolders}
			config := cfgadapters.TSLSConfig{TSServerFallbackPath: "", MaxTSServerMemory: 1536}
			expectedServerOptions := map[string]any{"useSyntaxServer": "never"}
			if withFallback {
				config.TSServerFallbackPath = filepath.Join(s.T().TempDir(), "typescript", "lib", "tsserver.js")
				expectedServerOptions["fallbackPath"] = config.TSServerFallbackPath
			}

			s.Require().NoError(patchInitializeParams(workspaceRoot, config, params))
			s.Equal(uri.File(workspaceRoot), params.RootURI)
			s.Empty(params.RootPath)
			s.Equal(workspaceFolders, params.WorkspaceFolders)
			s.Equal(map[string]any{
				"maxTsServerMemory": 1536,
				"tsserver":          expectedServerOptions,
			}, params.InitializationOptions)
		})
	}
}
