package lsptsls

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// TestPatchInitializeParamsSetsRootURIAndFallback proves that the TypeScript adapter preserves workspace folders,
// adds the legacy root URI, and delegates fallback validation to typescript-language-server.
func TestPatchInitializeParamsSetsRootURIAndFallback(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	workspaceFolders := []protocol.WorkspaceFolder{{
		URI:  string(uri.File(workspaceRoot)),
		Name: "workspace",
	}}
	//nolint:exhaustruct_v5 // The test exercises only workspace-root initialization fields.
	params := &protocol.InitializeParams{WorkspaceFolders: workspaceFolders}
	fallbackPath := filepath.Join(t.TempDir(), "missing", "typescript", "lib", "tsserver.js")

	err := patchInitializeParams(workspaceRoot, fallbackPath, params)
	require.NoError(t, err)

	assert.Equal(t, uri.File(workspaceRoot), params.RootURI)
	assert.Empty(t, params.RootPath)
	assert.Equal(t, workspaceFolders, params.WorkspaceFolders)
	assert.Equal(t, map[string]any{
		"tsserver": map[string]any{
			"fallbackPath": fallbackPath,
		},
	}, params.InitializationOptions)
}
