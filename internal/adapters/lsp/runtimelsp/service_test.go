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
		LSPConfig: LSPConfig{
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
		},
		BuildWorkspaceFolders: nil,
	})
	require.NoError(t, err)
	require.NoError(t, runtime.beginEnsureConn())

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- runtime.Close(t.Context())
	}()

	require.Eventually(t, func() bool {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()

		return runtime.closing
	}, time.Second, 10*time.Millisecond)

	require.ErrorIs(t, runtime.beginEnsureConn(), errRuntimeClosing)

	select {
	case err := <-closeDone:
		t.Fatalf("close returned early: %v", err)
	default:
	}

	runtime.endEnsureConn()
	require.NoError(t, <-closeDone)
}

// TestCloseRespectsContextWhileWaitingForEnsureConn proves that shutdown does not wait forever for a stuck EnsureConn.
func TestCloseRespectsContextWhileWaitingForEnsureConn(t *testing.T) {
	t.Parallel()

	runtime, err := New(&RuntimeConfig{
		LSPConfig: LSPConfig{
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
		},
		BuildWorkspaceFolders: nil,
	})
	require.NoError(t, err)
	require.NoError(t, runtime.beginEnsureConn())

	closeCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	closeErr := runtime.Close(closeCtx)
	require.ErrorIs(t, closeErr, context.DeadlineExceeded)

	runtime.endEnsureConn()
	require.NoError(t, runtime.beginEnsureConn())
	runtime.endEnsureConn()
}
