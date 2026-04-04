package helpers

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveCacheRootRejectsRelativePath proves that managed cache placement never depends on process cwd.
func TestResolveCacheRootRejectsRelativePath(t *testing.T) {
	t.Parallel()

	_, err := ResolveCacheRoot("relative/cache")
	require.Error(t, err)
	assert.ErrorContains(t, err, "must be absolute")
}

// TestResolveCacheRootCleansAbsolutePath proves that managed cache placement stays deterministic for one configured root.
func TestResolveCacheRootCleansAbsolutePath(t *testing.T) {
	t.Parallel()

	resolved, err := ResolveCacheRoot(filepath.Join(string(filepath.Separator), "tmp", "asteria", "..", "asteria", "cache"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(string(filepath.Separator), "tmp", "asteria", "cache"), resolved)
}

// TestWorkspaceHashIsStable proves that one normalized workspace root always maps to one stable cache namespace.
func TestWorkspaceHashIsStable(t *testing.T) {
	t.Parallel()

	firstHash := WorkspaceHash("/tmp/workspace")
	secondHash := WorkspaceHash("/tmp/workspace")
	otherHash := WorkspaceHash("/tmp/other-workspace")

	assert.Equal(t, firstHash, secondHash)
	assert.NotEqual(t, firstHash, otherHash)
}

// TestAdapterCacheDirBuildsManagedPath proves that adapter cache directories stay isolated by workspace hash and adapter name.
func TestAdapterCacheDirBuildsManagedPath(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	cacheRoot := filepath.Join(string(filepath.Separator), "tmp", "asteria", "cache")
	normalizedWorkspaceRoot, err := ResolveWorkspaceRoot(workspaceRoot)
	require.NoError(t, err)

	cacheDir, err := AdapterCacheDir(cacheRoot, workspaceRoot, "clangd")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cacheRoot, WorkspaceHash(normalizedWorkspaceRoot), "adapters", "clangd"), cacheDir)
}
