package lspphpactor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// TestBuildClientCapabilities proves that phpactor explicitly advertises the hover and references features
// that its adapter relies on, while preserving the shared runtime baseline for the rest of the handshake.
func TestBuildClientCapabilities(t *testing.T) {
	t.Parallel()

	capabilities := buildClientCapabilities()
	baseline := runtimelsp.DefaultClientCapabilities()

	require.NotNil(t, capabilities.TextDocument)
	require.NotNil(t, capabilities.TextDocument.Hover)
	require.NotNil(t, capabilities.TextDocument.References)
	assert.Equal(t, baseline.Workspace, capabilities.Workspace)
	assert.Equal(t, baseline.TextDocument.Definition, capabilities.TextDocument.Definition)
	assert.Equal(t, baseline.TextDocument.DocumentSymbol, capabilities.TextDocument.DocumentSymbol)
	assert.True(t, capabilities.TextDocument.Hover.DynamicRegistration)
	assert.Equal(
		t,
		[]protocol.MarkupKind{protocol.Markdown, protocol.PlainText},
		capabilities.TextDocument.Hover.ContentFormat,
	)
	assert.True(t, capabilities.TextDocument.References.DynamicRegistration)
}

// TestShouldWatchPHPFile proves that runtime-managed watcher notifications stay scoped to PHP files only.
func TestShouldWatchPHPFile(t *testing.T) {
	t.Parallel()

	assert.True(t, shouldWatchPHPFile("src/fixture.php"))
	assert.True(t, shouldWatchPHPFile("src/FIXTURE.PHP"))
	assert.False(t, shouldWatchPHPFile("src/fixture.txt"))
	assert.False(t, shouldWatchPHPFile("src/fixture"))
}

// TestNormalizeFoundSymbolInfo proves that phpactor's placeholder hover text falls back to the declaration line
// so callers still receive meaningful class info.
func TestNormalizeFoundSymbolInfo(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	filePath := filepath.Join(workspaceRoot, "fixture.php")
	require.NoError(t, os.WriteFile(filePath, []byte("<?php\n\nclass Bucket\n{\n}\n"), 0o600))

	info, err := normalizeFoundSymbolInfo(workspaceRoot, &domain.FoundSymbol{
		Kind:      int(protocol.SymbolKindClass),
		Body:      "",
		Info:      "Could not find source with \"Bucket\"",
		Path:      "Bucket",
		File:      "fixture.php",
		StartLine: 2,
		EndLine:   4,
	})
	require.NoError(t, err)
	assert.Equal(t, "class Bucket", info)
}

// TestPatchInitializeParams proves that the phpactor init hook keeps the deprecated startup workaround scoped
// to RootURI only while leaving the shared workspace-folder contract untouched and moving state into cache.
func TestPatchInitializeParams(t *testing.T) {
	t.Parallel()

	cacheRoot := t.TempDir()
	workspaceRoot := t.TempDir()

	workspaceFolders := []protocol.WorkspaceFolder{{URI: string(uri.File(workspaceRoot)), Name: "workspace"}}
	//nolint:exhaustruct // protocol.InitializeParams has many optional SDK fields that this unit test does not exercise.
	params := &protocol.InitializeParams{WorkspaceFolders: workspaceFolders}

	service := &Service{
		rt:                  nil,
		std:                 nil,
		withRequestDocument: nil,
		cacheRoot:           cacheRoot,
	}
	err := service.patchInitializeParams(workspaceRoot, params)
	require.NoError(t, err)

	expectedCacheDir, err := helpers.AdapterCacheDir(cacheRoot, workspaceRoot, phpactorServerName)
	require.NoError(t, err)

	assert.Equal(t, workspaceFolders, params.WorkspaceFolders)
	assert.Empty(t, params.RootPath)
	assert.Equal(t, uri.File(workspaceRoot), params.RootURI)
	assert.Equal(t, map[string]any{
		phpactorIndexerPathKey:       filepath.Join(expectedCacheDir, phpactorIndexerDirName),
		phpactorPHPStanEnabledKey:    false,
		phpactorPsalmEnabledKey:      false,
		phpactorPHPCSFixerEnabledKey: false,
	}, params.InitializationOptions)
}

// TestEnsureIndexerPathExists proves that phpactor startup prepares its managed cache directory without writing
// hidden state into the analyzed workspace.
func TestEnsureIndexerPathExists(t *testing.T) {
	t.Parallel()

	cacheRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	expectedCacheDir, err := helpers.AdapterCacheDir(cacheRoot, workspaceRoot, phpactorServerName)
	require.NoError(t, err)
	indexPath := filepath.Join(expectedCacheDir, phpactorIndexerDirName)

	require.NoError(t, ensureIndexerPathExists(cacheRoot, workspaceRoot))
	assert.DirExists(t, indexPath)

	_, statErr := os.Stat(filepath.Join(workspaceRoot, ".phpactor"))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

// TestShouldIgnoreDir keeps PHP workspace traversal away from hidden, cache, node_modules, and vendor folders.
func TestShouldIgnoreDir(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		relativePath string
		expected     bool
	}{
		{name: "hidden directory", relativePath: ".cache", expected: true},
		{name: "nested hidden directory", relativePath: "pkg/.git", expected: true},
		{name: "node modules directory", relativePath: "pkg/node_modules", expected: true},
		{name: "cache directory", relativePath: "pkg/cache", expected: true},
		{name: "vendor directory", relativePath: "vendor", expected: true},
		{name: "source directory", relativePath: "src", expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, testCase.expected, shouldIgnoreDir(testCase.relativePath))
		})
	}
}
