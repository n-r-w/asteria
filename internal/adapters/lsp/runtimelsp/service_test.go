package runtimelsp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
)

// TestCloseRejectsNewEnsureConnCalls proves that runtime shutdown blocks new connection requests until close finishes.
func TestCloseRejectsNewEnsureConnCalls(t *testing.T) {
	t.Parallel()

	runtime, err := New(&RuntimeConfig{
		Command:                 "gopls",
		Args:                    nil,
		ServerName:              "gopls",
		ShutdownTimeout:         time.Second,
		ReplyConfiguration:      nil,
		BuildClientCapabilities: func() protocol.ClientCapabilities { return protocol.ClientCapabilities{} },
		FileWatch:               nil,
		PatchInitializeParams:   nil,
		HandleServerCallback:    nil,
		AfterInitialized:        nil,
		WaitUntilReady:          nil,
		BuildWorkspaceFolders:   nil,
		SessionIdleTimeout:      0,
	})
	require.NoError(t, err)
	release, acquireErr := runtime.AcquireSession(t.Context(), t.TempDir())
	require.NoError(t, acquireErr)

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- runtime.Close(t.Context())
	}()

	require.Eventually(t, func() bool {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()

		return runtime.closing
	}, time.Second, 10*time.Millisecond)

	_, acquireErr = runtime.AcquireSession(t.Context(), t.TempDir())
	require.ErrorIs(t, acquireErr, errRuntimeClosing)

	select {
	case err := <-closeDone:
		t.Fatalf("close returned early: %v", err)
	default:
	}

	release()
	require.NoError(t, <-closeDone)
}

// TestCloseUsesShutdownTimeoutWhileWaitingForEnsureConn proves that runtime shutdown owns its cleanup budget
// instead of inheriting immediate caller cancellation from cleanup-time test contexts.
func TestCloseUsesShutdownTimeoutWhileWaitingForEnsureConn(t *testing.T) {
	t.Parallel()

	runtime, err := New(&RuntimeConfig{
		Command:                 "gopls",
		Args:                    nil,
		ServerName:              "gopls",
		ShutdownTimeout:         50 * time.Millisecond,
		ReplyConfiguration:      nil,
		BuildClientCapabilities: func() protocol.ClientCapabilities { return protocol.ClientCapabilities{} },
		FileWatch:               nil,
		PatchInitializeParams:   nil,
		HandleServerCallback:    nil,
		AfterInitialized:        nil,
		WaitUntilReady:          nil,
		BuildWorkspaceFolders:   nil,
		SessionIdleTimeout:      0,
	})
	require.NoError(t, err)
	root := t.TempDir()
	release, acquireErr := runtime.AcquireSession(t.Context(), root)
	require.NoError(t, acquireErr)

	closeCtx, cancel := context.WithCancel(t.Context())
	cancel()

	closeErr := runtime.Close(closeCtx)
	require.ErrorIs(t, closeErr, context.DeadlineExceeded)

	release()
	release, acquireErr = runtime.AcquireSession(t.Context(), root)
	require.NoError(t, acquireErr)
	release()
	require.NoError(t, runtime.Close(t.Context()))
}
