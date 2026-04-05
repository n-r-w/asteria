package lsprustanalyzer

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// newTestService creates a minimal Service instance for unit tests of specific methods
// without initializing the full LSP runtime. This is intentional - we test individual
// methods in isolation without dependencies on external processes.
func newTestService(cacheRoot string, cfg cfgadapters.RustAnalyzerConfig) *Service {
	return &Service{
		rt:                         nil,
		std:                        nil,
		stdReferences:              nil,
		withRequestDocument:        nil,
		cacheRoot:                  cacheRoot,
		mu:                         sync.Mutex{},
		startupReadiness:           make(map[string]*startupReadinessState),
		workspaceSymbolSearchLimit: cfg.WorkspaceSymbolSearchLimit,
		startupReadyTimeout:        cfg.StartupReadyTimeout,
	}
}

// TestBuildClientCapabilities proves that the Rust adapter enriches the runtime baseline only with the
// startup, indexing, symbol, hover, and reference capabilities that the live rust-analyzer probes exercised.
func TestBuildClientCapabilities(t *testing.T) {
	t.Parallel()

	capabilities := buildClientCapabilities()

	require.NotNil(t, capabilities.Workspace)
	require.NotNil(t, capabilities.Workspace.DidChangeWatchedFiles)
	assert.True(t, capabilities.Workspace.DidChangeWatchedFiles.DynamicRegistration)
	require.NotNil(t, capabilities.Workspace.ExecuteCommand)
	assert.True(t, capabilities.Workspace.ExecuteCommand.DynamicRegistration)
	require.NotNil(t, capabilities.Workspace.Symbol)
	assert.True(t, capabilities.Workspace.Symbol.DynamicRegistration)
	require.NotNil(t, capabilities.TextDocument)
	require.NotNil(t, capabilities.TextDocument.Synchronization)
	assert.True(t, capabilities.TextDocument.Synchronization.WillSave)
	assert.True(t, capabilities.TextDocument.Synchronization.WillSaveWaitUntil)
	assert.True(t, capabilities.TextDocument.Synchronization.DidSave)
	require.NotNil(t, capabilities.TextDocument.References)
	assert.True(t, capabilities.TextDocument.References.DynamicRegistration)
	require.NotNil(t, capabilities.TextDocument.Hover)
	assert.Equal(t, []protocol.MarkupKind{protocol.Markdown, protocol.PlainText}, capabilities.TextDocument.Hover.ContentFormat)
	require.NotNil(t, capabilities.TextDocument.PublishDiagnostics)
	assert.True(t, capabilities.TextDocument.PublishDiagnostics.RelatedInformation)
	require.NotNil(t, capabilities.Window)
	assert.True(t, capabilities.Window.WorkDoneProgress)
	require.IsType(t, map[string]any{}, capabilities.Experimental)
	experimental, ok := capabilities.Experimental.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, experimental["serverStatusNotification"])
}

// TestPatchInitializeParams proves that the Rust adapter adds workspace-loading and indexing options without
// restoring the deprecated root fields that the runtime intentionally removed.
func TestPatchInitializeParams(t *testing.T) {
	t.Parallel()

	cacheRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	service := newTestService(cacheRoot, cfgadapters.RustAnalyzerConfig{
		WorkspaceSymbolSearchLimit: 128,
		StartupReadyTimeout:        30 * time.Second,
	})

	workspaceFolders := []protocol.WorkspaceFolder{{URI: string(protocol.DocumentURI(filepath.ToSlash("file://" + workspaceRoot))), Name: "workspace"}}
	//nolint:exhaustruct // protocol.InitializeParams has many optional SDK fields that this unit test does not exercise.
	params := &protocol.InitializeParams{WorkspaceFolders: workspaceFolders}

	err := service.patchInitializeParams(workspaceRoot, params)
	require.NoError(t, err)

	expectedCacheDir, err := helpers.AdapterCacheDir(cacheRoot, workspaceRoot, rustAnalyzerServerName)
	require.NoError(t, err)

	assert.Equal(t, workspaceFolders, params.WorkspaceFolders)
	assert.Empty(t, params.RootPath)
	assert.Empty(t, params.RootURI)
	assert.Equal(t, map[string]any{
		"cargo": map[string]any{
			"autoreload": true,
			"extraEnv": map[string]any{
				"CARGO_TARGET_DIR": filepath.Join(expectedCacheDir, rustAnalyzerCargoTargetDirName),
			},
			"buildScripts": map[string]any{
				"enable":             true,
				"invocationLocation": "workspace",
				"invocationStrategy": "per_workspace",
			},
		},
		"procMacro": map[string]any{
			"enable": true,
			"attributes": map[string]any{
				"enable": true,
			},
		},
		"checkOnSave":    false,
		"linkedProjects": []any{},
		"workspace": map[string]any{
			"symbol": map[string]any{
				"search": map[string]any{
					"kind":  "only_types",
					"limit": 128,
					"scope": "workspace",
				},
			},
		},
		"diagnostics": map[string]any{
			"enable": true,
		},
	}, params.InitializationOptions)
}

// TestWaitUntilReadyReturnsAfterQuiescentStatus proves that startup waiting is released only by the observed
// quiescent rust-analyzer signal, not merely by seeing an earlier non-ready status notification.
func TestWaitUntilReadyReturnsAfterQuiescentStatus(t *testing.T) {
	t.Parallel()

	service := newTestService(t.TempDir(), cfgadapters.RustAnalyzerConfig{
		WorkspaceSymbolSearchLimit: 128,
		StartupReadyTimeout:        5 * time.Second,
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	waitErrCh := make(chan error, 1)
	go func() {
		waitErrCh <- service.waitUntilReady(ctx, nil, "/workspace")
	}()

	serviceReadyFalse := decodeNotification(t, `{"jsonrpc":"2.0","method":"experimental/serverStatus","params":{"health":"ok","quiescent":false}}`)
	handled, err := service.handleServerCallback(t.Context(), func(context.Context, any, error) error { return nil }, serviceReadyFalse, "/workspace")
	require.True(t, handled)
	require.NoError(t, err)

	select {
	case err = <-waitErrCh:
		t.Fatalf("waitUntilReady returned too early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	serviceReadyTrue := decodeNotification(t, `{"jsonrpc":"2.0","method":"experimental/serverStatus","params":{"health":"ok","quiescent":true}}`)
	handled, err = service.handleServerCallback(t.Context(), func(context.Context, any, error) error { return nil }, serviceReadyTrue, "/workspace")
	require.True(t, handled)
	require.NoError(t, err)
	require.NoError(t, <-waitErrCh)
	assert.Empty(t, service.startupReadiness)
}

// TestHandleServerCallbackAcknowledgesDiagnosticRefresh proves that the Rust adapter still accepts the
// workspace diagnostic refresh request even though readiness no longer depends on that request directly.
func TestHandleServerCallbackAcknowledgesDiagnosticRefresh(t *testing.T) {
	t.Parallel()

	service := newTestService(t.TempDir(), cfgadapters.RustAnalyzerConfig{
		WorkspaceSymbolSearchLimit: 128,
		StartupReadyTimeout:        30 * time.Second,
	})
	replied := false
	request := decodeCall(t, `{"jsonrpc":"2.0","id":1,"method":"workspace/diagnostic/refresh","params":null}`)

	handled, err := service.handleServerCallback(
		t.Context(),
		func(context.Context, any, error) error {
			replied = true
			return nil
		},
		request,
		"/workspace",
	)
	require.NoError(t, err)
	assert.True(t, handled)
	assert.True(t, replied)
}

// decodeCall keeps callback-handler tests explicit without depending on unexported jsonrpc2 constructors.
func decodeCall(t *testing.T, raw string) jsonrpc2.Request {
	t.Helper()

	var request jsonrpc2.Call
	require.NoError(t, json.Unmarshal([]byte(raw), &request))

	return &request
}

// decodeNotification keeps callback-handler notification tests short and readable.
func decodeNotification(t *testing.T, raw string) jsonrpc2.Request {
	t.Helper()

	var request jsonrpc2.Notification
	require.NoError(t, json.Unmarshal([]byte(raw), &request))

	return &request
}
