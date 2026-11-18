package lsprustanalyzer

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
)

// newTestService creates a minimal Service instance for unit tests of specific methods
// without initializing the full LSP runtime. This is intentional - we test individual
// methods in isolation without dependencies on external processes.
func newTestService(cacheRoot string, cfg cfgadapters.RustAnalyzerConfig) *Service {
	return &Service{
		rt:                         nil,
		ensureConn:                 nil,
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
	assert.True(t, lo.FromPtr(capabilities.Workspace.DidChangeWatchedFiles.DynamicRegistration))
	require.NotNil(t, capabilities.Workspace.ExecuteCommand)
	assert.True(t, lo.FromPtr(capabilities.Workspace.ExecuteCommand.DynamicRegistration))
	require.NotNil(t, capabilities.Workspace.Symbol)
	assert.True(t, lo.FromPtr(capabilities.Workspace.Symbol.DynamicRegistration))
	require.NotNil(t, capabilities.TextDocument)
	require.NotNil(t, capabilities.TextDocument.Synchronization)
	assert.True(t, lo.FromPtr(capabilities.TextDocument.Synchronization.WillSave))
	assert.True(t, lo.FromPtr(capabilities.TextDocument.Synchronization.WillSaveWaitUntil))
	assert.True(t, lo.FromPtr(capabilities.TextDocument.Synchronization.DidSave))
	require.NotNil(t, capabilities.TextDocument.References)
	assert.True(t, lo.FromPtr(capabilities.TextDocument.References.DynamicRegistration))
	require.NotNil(t, capabilities.TextDocument.Hover)
	assert.Equal(
		t,
		[]protocol.MarkupKind{protocol.MarkupKindMarkdown, protocol.MarkupKindPlainText},
		capabilities.TextDocument.Hover.ContentFormat,
	)
	require.NotNil(t, capabilities.TextDocument.PublishDiagnostics)
	assert.True(t, lo.FromPtr(capabilities.TextDocument.PublishDiagnostics.RelatedInformation))
	require.NotNil(t, capabilities.Window)
	assert.True(t, lo.FromPtr(capabilities.Window.WorkDoneProgress))
	assert.JSONEq(t, `{"serverStatusNotification":true}`, string(capabilities.Experimental))
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

	workspaceFolders := protocol.NewNullable([]protocol.WorkspaceFolder{
		{URI: uri.File(workspaceRoot), Name: "workspace"},
	})
	// protocol.InitializeParams has many optional SDK fields that this unit test does not
	// exercise.
	//nolint:exhaustruct_v5 // See the preceding comment.
	params := &protocol.InitializeParams{
		WorkspaceFolders: workspaceFolders,
	}

	err := service.patchInitializeParams(workspaceRoot, params)
	require.NoError(t, err)

	expectedCacheDir, err := helpers.AdapterCacheDir(cacheRoot, workspaceRoot, rustAnalyzerServerName)
	require.NoError(t, err)

	assert.Equal(t, workspaceFolders, params.WorkspaceFolders)
	assert.True(t, params.RootPath.IsZero())
	assert.Nil(t, params.RootURI)
	expectedOptions, err := json.Marshal(map[string]any{
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
			"enable": false,
		},
	})
	require.NoError(t, err)
	assert.JSONEq(t, string(expectedOptions), string(params.InitializationOptions))
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
	peerConn := serveServerCallback(t, service)

	require.NoError(t, peerConn.Notify(t.Context(), experimentalServerStatusMethod, map[string]any{
		"health": "ok", "quiescent": false,
	}))

	select {
	case err := <-waitErrCh:
		t.Fatalf("waitUntilReady returned too early: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	require.NoError(t, peerConn.Notify(t.Context(), experimentalServerStatusMethod, map[string]any{
		"health": "ok", "quiescent": true,
	}))
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
	peerConn := serveServerCallback(t, service)

	var result json.RawMessage
	_, err := peerConn.Call(t.Context(), workspaceDiagnosticRefreshMethod, nil, &result)
	require.NoError(t, err)
	assert.JSONEq(t, "null", string(result))
}

// serveServerCallback connects one in-memory peer whose messages reach the adapter server callback first;
// messages that the callback does not handle get the standard method-not-found answer.
func serveServerCallback(t *testing.T, service *Service) jsonrpc2.Conn {
	t.Helper()

	callbackSide, peerSide := net.Pipe()
	callbackConn := jsonrpc2.NewConn(jsonrpc2.NewStream(callbackSide))
	peerConn := jsonrpc2.NewConn(jsonrpc2.NewStream(peerSide))
	callbackConn.Go(t.Context(), func(ctx context.Context, req *jsonrpc2.Request) (any, error) {
		result, handled, err := service.handleServerCallback(ctx, req, "/workspace")
		if !handled {
			return jsonrpc2.MethodNotFoundHandler(ctx, req)
		}

		return result, err
	})
	peerConn.Go(t.Context(), jsonrpc2.MethodNotFoundHandler)
	t.Cleanup(func() {
		require.NoError(t, peerConn.Close())
		require.NoError(t, callbackConn.Close())
	})

	return peerConn
}

// TestWaitUntilReadyTimeoutNamesTimeoutSetting proves that the client learns which setting lets indexing finish.
func TestWaitUntilReadyTimeoutNamesTimeoutSetting(t *testing.T) {
	t.Parallel()

	service := newTestService(t.TempDir(), cfgadapters.RustAnalyzerConfig{
		WorkspaceSymbolSearchLimit: 128,
		StartupReadyTimeout:        10 * time.Millisecond,
	})

	err := service.waitUntilReady(t.Context(), nil, "/workspace")

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "within 10ms; increase ASTERIAMCP_RUST_ANALYZER_STARTUP_READY_TIMEOUT")
}
