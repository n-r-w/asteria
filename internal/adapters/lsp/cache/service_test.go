package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCacheFilePathUsesVersionedSharedNamespace proves that the shared cache lives under shared/v1,
// next to adapter-specific caches, not inside one adapter subtree.
func TestCacheFilePathUsesVersionedSharedNamespace(t *testing.T) {
	t.Parallel()

	cachePath := cacheFilePath("/cache-root", "/workspace", "clangd", "std", "pkg/demo.cpp")
	pathParts := strings.Split(filepath.ToSlash(cachePath), "/")

	assert.Contains(t, pathParts, sharedCacheDirName)
	assert.Contains(t, pathParts, cacheLayoutVersionDir)
	assert.Equal(t, []string{
		"cache-root",
		workspaceHash("/workspace"),
		sharedCacheDirName,
		cacheLayoutVersionDir,
		"clangd",
		"std",
		symbolTreeArtifactKind,
		filePathHash("pkg/demo.cpp") + cacheFileExtension,
	}, pathParts[1:])
}

const testCompileCommandsFileName = "compile_commands.json"

// TestServiceReadWriteSymbolTreeRoundTrip proves that one valid cache entry can be read back unchanged.
func TestServiceReadWriteSymbolTreeRoundTrip(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	writeTestFile(t, workspaceRoot, "pkg/demo.cpp", "int demo() { return 1; }\n")
	service, err := New(filepath.Join(t.TempDir(), "cache-root"))
	require.NoError(t, err)

	request := &stdlsp.WriteSymbolTreeCacheRequest{
		WorkspaceRoot: workspaceRoot,
		RelativePath:  "pkg/demo.cpp",
		Metadata: stdlsp.SymbolTreeCacheMetadata{
			Enabled:                true,
			DisabledReason:         "",
			AdapterID:              "clangd",
			ProfileID:              "std",
			AdapterFingerprint:     "fingerprint-1",
			AdditionalDependencies: nil,
		},
		Payload: []byte(`{"kind":"symbol-tree"}`),
	}
	require.NoError(t, service.WriteSymbolTree(t.Context(), request))

	payload, found, err := service.ReadSymbolTree(t.Context(), &stdlsp.ReadSymbolTreeCacheRequest{
		WorkspaceRoot: workspaceRoot,
		RelativePath:  "pkg/demo.cpp",
		Metadata:      request.Metadata,
	})
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, request.Payload, payload)
}

// TestServiceReadSymbolTreeInvalidatesChangedDependency proves that dependency content participates in cache validation.
func TestServiceReadSymbolTreeInvalidatesChangedDependency(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	writeTestFile(t, workspaceRoot, "pkg/demo.cpp", "int demo() { return 1; }\n")
	writeTestFile(t, workspaceRoot, testCompileCommandsFileName, "[]\n")
	service, err := New(filepath.Join(t.TempDir(), "cache-root"))
	require.NoError(t, err)

	metadata := stdlsp.SymbolTreeCacheMetadata{
		Enabled:                true,
		DisabledReason:         "",
		AdapterID:              "clangd",
		ProfileID:              "std",
		AdapterFingerprint:     "fingerprint-1",
		AdditionalDependencies: []string{testCompileCommandsFileName},
	}
	require.NoError(t, service.WriteSymbolTree(t.Context(), &stdlsp.WriteSymbolTreeCacheRequest{
		WorkspaceRoot: workspaceRoot,
		RelativePath:  "pkg/demo.cpp",
		Metadata:      metadata,
		Payload:       []byte(`payload`),
	}))

	writeTestFile(t, workspaceRoot, testCompileCommandsFileName, "[{\"changed\":true}]\n")
	payload, found, err := service.ReadSymbolTree(t.Context(), &stdlsp.ReadSymbolTreeCacheRequest{
		WorkspaceRoot: workspaceRoot,
		RelativePath:  "pkg/demo.cpp",
		Metadata:      metadata,
	})
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, payload)
}

// TestServiceReadSymbolTreeInvalidatesDifferentFingerprint proves that adapter fingerprint changes invalidate old entries.
func TestServiceReadSymbolTreeInvalidatesDifferentFingerprint(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	writeTestFile(t, workspaceRoot, "pkg/demo.cpp", "int demo() { return 1; }\n")
	service, err := New(filepath.Join(t.TempDir(), "cache-root"))
	require.NoError(t, err)

	writeMetadata := stdlsp.SymbolTreeCacheMetadata{
		Enabled:                true,
		DisabledReason:         "",
		AdapterID:              "clangd",
		ProfileID:              "std",
		AdapterFingerprint:     "fingerprint-1",
		AdditionalDependencies: nil,
	}
	require.NoError(t, service.WriteSymbolTree(t.Context(), &stdlsp.WriteSymbolTreeCacheRequest{
		WorkspaceRoot: workspaceRoot,
		RelativePath:  "pkg/demo.cpp",
		Metadata:      writeMetadata,
		Payload:       []byte(`payload`),
	}))

	payload, found, err := service.ReadSymbolTree(t.Context(), &stdlsp.ReadSymbolTreeCacheRequest{
		WorkspaceRoot: workspaceRoot,
		RelativePath:  "pkg/demo.cpp",
		Metadata: stdlsp.SymbolTreeCacheMetadata{
			Enabled:                true,
			DisabledReason:         "",
			AdapterID:              "clangd",
			ProfileID:              "std",
			AdapterFingerprint:     "fingerprint-2",
			AdditionalDependencies: nil,
		},
	})
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, payload)
}

func writeTestFile(t *testing.T, workspaceRoot, relativePath, content string) {
	t.Helper()

	absolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(relativePath))
	require.NoError(t, os.MkdirAll(filepath.Dir(absolutePath), 0o755))
	require.NoError(t, os.WriteFile(absolutePath, []byte(content), 0o600))
}
