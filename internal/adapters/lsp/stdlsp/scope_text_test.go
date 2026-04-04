package stdlsp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
)

// TestCollectScopeFilesAppliesAdapterIgnoreRules proves that stdlsp owns directory walking
// while adapters still control which directories should be skipped.
func TestCollectScopeFilesAppliesAdapterIgnoreRules(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceRoot, ".hidden", "nested"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceRoot, "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceRoot, ".hidden", "nested", "ignored.go"), []byte("package hidden\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(workspaceRoot, "pkg", "kept.go"), []byte("package pkg\n"), 0o600))

	scope := searchScope{RelativePath: "", AbsolutePath: workspaceRoot, IsDir: true}
	files, err := collectScopeFiles(workspaceRoot, scope, []string{".go"}, func(relativePath string) bool {
		return filepath.Base(relativePath) == ".hidden"
	})
	require.NoError(t, err)
	assert.Equal(t, []string{filepath.Join("pkg", "kept.go")}, files)
}

// TestReferenceEvidenceCandidateFromRangeReadsSnippet proves that stdlsp keeps snippet shaping
// close to the shared text helper instead of duplicating it in adapters.
func TestReferenceEvidenceCandidateFromRangeReadsSnippet(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	relativePath := "fixture.go"
	content := "package fixture\n\nfunc Use() {\n\tMakeBucket()\n}\n"
	require.NoError(t, os.WriteFile(filepath.Join(workspaceRoot, relativePath), []byte(content), 0o600))

	evidence, err := referenceEvidenceCandidateFromRange(
		workspaceRoot,
		relativePath,
		protocol.Range{
			Start: protocol.Position{Line: 3, Character: 1},
			End:   protocol.Position{Line: 3, Character: 11},
		},
		map[string]string{},
	)
	require.NoError(t, err)
	assert.Equal(t, 3, evidence.StartLine)
	assert.Equal(t, 3, evidence.EndLine)
	assert.Equal(t, 2, evidence.ContentStartLine)
	assert.Equal(t, 4, evidence.ContentEndLine)
	assert.Equal(t, 2, evidence.Column)
	assert.Contains(t, evidence.Content, "func Use() {")
	assert.Contains(t, evidence.Content, "\tMakeBucket()")
	assert.Contains(t, evidence.Content, "}")
}
