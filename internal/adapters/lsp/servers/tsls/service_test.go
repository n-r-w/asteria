package lsptsls

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// TestPatchInitializeParamsSetsRootURI proves that the TypeScript adapter preserves the shared workspace-folder
// payload while adding the legacy root URI required by supported typescript-language-server versions.
func TestPatchInitializeParamsSetsRootURI(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	workspaceFolders := []protocol.WorkspaceFolder{{
		URI:  string(uri.File(workspaceRoot)),
		Name: "workspace",
	}}
	//nolint:exhaustruct_v5 // The test exercises only workspace-root initialization fields.
	params := &protocol.InitializeParams{WorkspaceFolders: workspaceFolders}

	err := patchInitializeParams(workspaceRoot, params)
	require.NoError(t, err)

	assert.Equal(t, uri.File(workspaceRoot), params.RootURI)
	assert.Empty(t, params.RootPath)
	assert.Equal(t, workspaceFolders, params.WorkspaceFolders)
}
