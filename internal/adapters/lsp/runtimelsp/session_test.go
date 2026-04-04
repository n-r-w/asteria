package runtimelsp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// TestInitializeParamsUseRuntimeBaseline proves that the default runtime handshake no longer sends deprecated
// root fields and still advertises the baseline capabilities needed by standard-LSP adapters.
func TestInitializeParamsUseRuntimeBaseline(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()
	normalizedWorkspaceRoot, normalizeErr := normalizeWorkspaceRoot(workspaceRoot)
	require.NoError(t, normalizeErr)

	runtime := newTestRuntime(t, nil)

	session, sessionErr := runtime.getOrCreateSession(workspaceRoot)
	require.NoError(t, sessionErr)

	initializeParams, err := session.initializeParams(42)
	require.NoError(t, err)

	assert.Empty(t, initializeParams.RootPath)
	assert.Empty(t, initializeParams.RootURI)
	assert.Equal(t, int32(42), initializeParams.ProcessID)
	assert.Equal(t, protocol.TraceOff, initializeParams.Trace)
	assert.Equal(t, "en", initializeParams.Locale)
	require.Len(t, initializeParams.WorkspaceFolders, 1)
	assert.Equal(t, string(uri.File(normalizedWorkspaceRoot)), initializeParams.WorkspaceFolders[0].URI)
	assert.Equal(t, filepath.Base(normalizedWorkspaceRoot), initializeParams.WorkspaceFolders[0].Name)

	require.NotNil(t, initializeParams.Capabilities.Workspace)
	assert.True(t, initializeParams.Capabilities.Workspace.WorkspaceFolders)
	assert.True(t, initializeParams.Capabilities.Workspace.Configuration)
	require.NotNil(t, initializeParams.Capabilities.TextDocument)
	require.NotNil(t, initializeParams.Capabilities.TextDocument.DocumentSymbol)
	assert.True(t, initializeParams.Capabilities.TextDocument.DocumentSymbol.HierarchicalDocumentSymbolSupport)
}

// TestInitializeParamsUseCustomClientCapabilities proves that RuntimeConfig has a real per-adapter override path
// instead of forcing every standard-LSP adapter onto the same hardcoded capability profile.
func TestInitializeParamsUseCustomClientCapabilities(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()

	customBuilderCalls := 0
	runtime := newTestRuntime(t, func(cfg *LSPConfig) {
		cfg.BuildClientCapabilities = func() protocol.ClientCapabilities {
			customBuilderCalls++

			return protocol.ClientCapabilities{
				Workspace:    nil,
				TextDocument: nil,
				Window: &protocol.WindowClientCapabilities{
					WorkDoneProgress: true,
					ShowMessage:      nil,
					ShowDocument:     nil,
				},
				General:      nil,
				Experimental: nil,
			}
		}
	})

	session, sessionErr := runtime.getOrCreateSession(workspaceRoot)
	require.NoError(t, sessionErr)

	initializeParams, err := session.initializeParams(7)
	require.NoError(t, err)

	assert.Equal(t, 1, customBuilderCalls)
	assert.Nil(t, initializeParams.Capabilities.Workspace)
	assert.Nil(t, initializeParams.Capabilities.TextDocument)
	require.NotNil(t, initializeParams.Capabilities.Window)
	assert.True(t, initializeParams.Capabilities.Window.WorkDoneProgress)
}

// TestInitializeParamsApplyAdapterPatch proves that one adapter can enrich the shared initialize payload
// without forking the whole runtime handshake.
func TestInitializeParamsApplyAdapterPatch(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()
	normalizedWorkspaceRoot, normalizeErr := normalizeWorkspaceRoot(workspaceRoot)
	require.NoError(t, normalizeErr)

	runtime := newTestRuntime(t, func(cfg *LSPConfig) {
		cfg.PatchInitializeParams = func(root string, params *protocol.InitializeParams) error {
			require.Equal(t, normalizedWorkspaceRoot, root)
			params.RootPath = root
			params.RootURI = uri.File(root)
			params.InitializationOptions = map[string]any{"probe": true}

			return nil
		}
	})

	session, sessionErr := runtime.getOrCreateSession(workspaceRoot)
	require.NoError(t, sessionErr)

	initializeParams, err := session.initializeParams(11)
	require.NoError(t, err)

	assert.Equal(t, normalizedWorkspaceRoot, initializeParams.RootPath)
	assert.Equal(t, uri.File(normalizedWorkspaceRoot), initializeParams.RootURI)
	require.NotNil(t, initializeParams.InitializationOptions)
	optionsJSON, marshalErr := json.Marshal(initializeParams.InitializationOptions)
	require.NoError(t, marshalErr)
	assert.JSONEq(t, `{"probe":true}`, string(optionsJSON))
}

// TestInitializeParamsAdvertiseWatchedFiles proves that sessions with runtime-managed file watching
// advertise the standard LSP watched-files capability during the initialize handshake.
func TestInitializeParamsAdvertiseWatchedFiles(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()

	runtime := newTestRuntime(t, func(cfg *LSPConfig) {
		cfg.FileWatch = &FileWatchConfig{
			RelevantFile: func(string) bool { return true },
			IgnoreDir:    nil,
		}
	})

	session, sessionErr := runtime.getOrCreateSession(workspaceRoot)
	require.NoError(t, sessionErr)

	initializeParams, err := session.initializeParams(5)
	require.NoError(t, err)

	require.NotNil(t, initializeParams.Capabilities.Workspace)
	require.NotNil(t, initializeParams.Capabilities.Workspace.DidChangeWatchedFiles)
	assert.True(t, initializeParams.Capabilities.Workspace.DidChangeWatchedFiles.DynamicRegistration)
}

// TestGetOrCreateSessionReusesNormalizedRoot proves that equivalent root paths reuse one session key.
func TestGetOrCreateSessionReusesNormalizedRoot(t *testing.T) {
	t.Parallel()

	realRoot := t.TempDir()
	symlinkPath := filepath.Join(t.TempDir(), "workspace-link")
	require.NoError(t, os.Symlink(realRoot, symlinkPath))

	runtime := newTestRuntime(t, nil)

	firstSession, firstErr := runtime.getOrCreateSession(realRoot)
	require.NoError(t, firstErr)
	secondSession, secondErr := runtime.getOrCreateSession(symlinkPath)
	require.NoError(t, secondErr)

	assert.Same(t, firstSession, secondSession)
	require.Len(t, runtime.sessions, 1)
}

// TestGetOrCreateSessionSeparatesDifferentRoots proves that each normalized root gets its own session wrapper.
func TestGetOrCreateSessionSeparatesDifferentRoots(t *testing.T) {
	t.Parallel()

	firstRoot := t.TempDir()
	secondRoot := t.TempDir()

	runtime := newTestRuntime(t, nil)

	firstSession, firstErr := runtime.getOrCreateSession(firstRoot)
	require.NoError(t, firstErr)
	secondSession, secondErr := runtime.getOrCreateSession(secondRoot)
	require.NoError(t, secondErr)

	assert.NotSame(t, firstSession, secondSession)
	require.Len(t, runtime.sessions, 2)
}

// newTestRuntime keeps the session tests focused on the behavior under test instead of repeated runtime setup.
func newTestRuntime(t *testing.T, configure func(*LSPConfig)) *Runtime {
	t.Helper()

	config := LSPConfig{
		Command:                 "gopls",
		Args:                    nil,
		ServerName:              "gopls",
		ShutdownTimeout:         0,
		ReplyConfiguration:      nil,
		BuildClientCapabilities: nil,
		FileWatch:               nil,
		PatchInitializeParams:   nil,
		HandleServerCallback:    nil,
		AfterInitialized:        nil,
		WaitUntilReady:          nil,
	}
	if configure != nil {
		configure(&config)
	}

	runtime, err := New(&RuntimeConfig{
		LSPConfig:             config,
		BuildWorkspaceFolders: nil,
	})
	require.NoError(t, err)

	return runtime
}
