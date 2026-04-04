package lspclangd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
)

// TestClangdWorkspaceDependenciesAddsBenignClangdConfig proves that a local .clangd file
// becomes a cache dependency instead of disabling cache on its mere existence.
func TestClangdWorkspaceDependenciesAddsBenignClangdConfig(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, ".clangd"),
		[]byte("If:\n  PathMatch: .*\\.cpp\nIndex:\n  Background: Build\n"),
		clangdManagedFilePermissions,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, compileCommandsFileName),
		[]byte("[]"),
		clangdManagedFilePermissions,
	))

	dependencies, disabledReason := clangdWorkspaceDependencies(workspaceRoot, "src/demo.cpp")
	assert.Empty(t, disabledReason)
	assert.Equal(t, []string{".clangd", compileCommandsFileName}, dependencies)
}

// TestClangdWorkspaceDependenciesDisablesExternalCompilationDatabase proves that cache stays off
// when the matching .clangd fragment points at a compilation database outside the workspace.
func TestClangdWorkspaceDependenciesDisablesExternalCompilationDatabase(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, ".clangd"),
		[]byte("CompileFlags:\n  CompilationDatabase: /tmp/external-build\n"),
		clangdManagedFilePermissions,
	))

	dependencies, disabledReason := clangdWorkspaceDependencies(workspaceRoot, "src/demo.cpp")
	assert.Nil(t, dependencies)
	assert.Equal(t, cacheDisabledExternalClangdConfig, disabledReason)
}

// TestClangdWorkspaceDependenciesScopesCompilationDatabaseByPathMatch proves that non-matching
// .clangd fragments do not disable cache for unrelated files.
func TestClangdWorkspaceDependenciesScopesCompilationDatabaseByPathMatch(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceRoot, "build"), clangdCacheDirPermissions))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, "build", compileCommandsFileName),
		[]byte("[]"),
		clangdManagedFilePermissions,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, ".clangd"),
		[]byte("If:\n  PathMatch: .*special.*\\.cpp\nCompileFlags:\n  CompilationDatabase: build\n"),
		clangdManagedFilePermissions,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, compileFlagsFileName),
		[]byte("-std=c++20\n"),
		clangdManagedFilePermissions,
	))

	normalDependencies, normalReason := clangdWorkspaceDependencies(workspaceRoot, "src/demo.cpp")
	assert.Empty(t, normalReason)
	assert.Equal(t, []string{".clangd", compileFlagsFileName}, normalDependencies)

	specialDependencies, specialReason := clangdWorkspaceDependencies(workspaceRoot, "src/special_case.cpp")
	assert.Empty(t, specialReason)
	assert.Equal(t, []string{".clangd", filepath.Join("build", compileCommandsFileName)}, specialDependencies)
}

// TestLanguageIDForExtension keeps the didOpen language mapping explicit for C and C++ file families.
func TestLanguageIDForExtension(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		extension string
		expected  string
	}{
		{name: "c source", extension: ".c", expected: clangdLanguageIDC},
		{name: "c plus plus source", extension: ".cpp", expected: clangdLanguageIDCPP},
		{name: "uppercase c means c plus plus", extension: ".C", expected: clangdLanguageIDCPP},
		{name: "header defaults to c plus plus", extension: ".h", expected: clangdLanguageIDCPP},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, testCase.expected, languageIDForExtension(testCase.extension))
		})
	}
}

// TestShouldWatchClangdFile keeps live workspace notifications focused on
// supported source files and compile config files.
func TestShouldWatchClangdFile(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		path     string
		expected bool
	}{
		{name: "cpp source", path: "src/fixture.cpp", expected: true},
		{name: "header", path: "include/fixture.hpp", expected: true},
		{name: "module interface", path: "src/fixture.cppm", expected: true},
		{name: "compile commands", path: "compile_commands.json", expected: true},
		{name: "compile flags", path: "compile_flags.txt", expected: true},
		{name: "other json", path: "config.json", expected: false},
		{name: "markdown", path: "README.md", expected: false},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, testCase.expected, shouldWatchClangdFile(testCase.path))
		})
	}
}

// TestPrepareManagedCompilationDatabaseCopiesCompileFlags proves that clangd can
// be pointed at a managed compile_flags fallback.
func TestPrepareManagedCompilationDatabaseCopiesCompileFlags(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "clangd")
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, compileFlagsFileName),
		[]byte("-std=c++20\n-Iinclude\n"),
		clangdManagedFilePermissions,
	))

	hasDatabase, err := prepareManagedCompilationDatabase(workspaceRoot, cacheDir)
	require.NoError(t, err)
	assert.True(t, hasDatabase)

	content, err := os.ReadFile(filepath.Join(cacheDir, compileFlagsFileName))
	require.NoError(t, err)
	assert.Equal(t, "-std=c++20\n-Iinclude\n", string(content))
}

// TestPrepareManagedCompilationDatabasePrefersCompileCommands proves that
// compile_commands.json stays the primary source when both files exist.
func TestPrepareManagedCompilationDatabasePrefersCompileCommands(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "clangd")
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, compileFlagsFileName),
		[]byte("-std=c++20\n"),
		clangdManagedFilePermissions,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, compileCommandsFileName),
		[]byte("[]"),
		clangdManagedFilePermissions,
	))

	hasDatabase, err := prepareManagedCompilationDatabase(workspaceRoot, cacheDir)
	require.NoError(t, err)
	assert.True(t, hasDatabase)
	_, commandsErr := os.Stat(filepath.Join(cacheDir, compileCommandsFileName))
	require.NoError(t, commandsErr)
	_, flagsErr := os.Stat(filepath.Join(cacheDir, compileFlagsFileName))
	assert.Error(t, flagsErr)
}

// TestPrepareManagedCompilationDatabaseAbsolutizesDirectory proves that managed
// compile_commands keep relative directories workspace-stable.
func TestPrepareManagedCompilationDatabaseAbsolutizesDirectory(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "clangd")
	payload := []compileCommandEntry{{
		Directory: ".",
		File:      "fixture.cpp",
		Arguments: []string{"clang++", "-c", "fixture.cpp"},
		Command:   "",
		Output:    "",
	}}
	encodedPayload, err := json.Marshal(payload)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, compileCommandsFileName),
		encodedPayload,
		clangdManagedFilePermissions,
	))

	hasDatabase, err := prepareManagedCompilationDatabase(workspaceRoot, cacheDir)
	require.NoError(t, err)
	assert.True(t, hasDatabase)

	materializedPayload, err := os.ReadFile(filepath.Join(cacheDir, compileCommandsFileName))
	require.NoError(t, err)

	var entries []compileCommandEntry
	require.NoError(t, json.Unmarshal(materializedPayload, &entries))
	require.Len(t, entries, 1)
	normalizedWorkspaceRoot, err := helpers.ResolveWorkspaceRoot(workspaceRoot)
	require.NoError(t, err)
	assert.Equal(t, normalizedWorkspaceRoot, entries[0].Directory)
}

// TestPatchInitializeParamsSetsCompilationDatabasePath proves that clangd can
// consume the managed compile database through initialize params when the
// adapter materialized one.
func TestPatchInitializeParamsSetsCompilationDatabasePath(t *testing.T) {
	t.Parallel()

	service := &Service{
		rt:                  nil,
		std:                 nil,
		withRequestDocument: nil,
		cacheRoot:           filepath.Join(string(filepath.Separator), "tmp", "asteria", "cache"),
	}
	workspaceRoot := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, compileFlagsFileName),
		[]byte("-std=c++20\n-Iinclude\n"),
		clangdManagedFilePermissions,
	))
	cacheDir, err := service.cacheDir(workspaceRoot)
	require.NoError(t, err)

	params := &protocol.InitializeParams{}
	err = service.patchInitializeParams(workspaceRoot, params)
	require.NoError(t, err)

	options, ok := params.InitializationOptions.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, cacheDir, options[clangdCompilationDatabasePathKey])
}

// TestPatchInitializeParamsSkipsCompilationDatabasePathWithoutManagedDatabase
// proves that clangd keeps its own compilation-database discovery when the
// adapter did not materialize a managed database.
func TestPatchInitializeParamsSkipsCompilationDatabasePathWithoutManagedDatabase(t *testing.T) {
	t.Parallel()

	service := &Service{
		rt:                  nil,
		std:                 nil,
		withRequestDocument: nil,
		cacheRoot:           filepath.Join(string(filepath.Separator), "tmp", "asteria", "cache"),
	}
	workspaceRoot := t.TempDir()

	params := &protocol.InitializeParams{}
	err := service.patchInitializeParams(workspaceRoot, params)
	require.NoError(t, err)
	assert.Nil(t, params.InitializationOptions)
}
