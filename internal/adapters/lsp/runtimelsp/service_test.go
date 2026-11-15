package runtimelsp

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
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

// backgroundCloseSuite checks how a runtime closes a session in the background.
type backgroundCloseSuite struct {
	suite.Suite
}

// TestBackgroundCloseSuite runs the checks of closing a session in the background.
func TestBackgroundCloseSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(backgroundCloseSuite))
}

// TestReturnsBeforeCloseAndStartsNewSession proves that the close returns while the old session still shuts down and
// that the next access to the workspace gets a new session at once.
func (s *backgroundCloseSuite) TestReturnsBeforeCloseAndStartsNewSession() {
	synctest.Test(s.T(), func(t *testing.T) {
		runtime := newTestRuntime(t, nil)
		root := t.TempDir()
		oldSession, serverExited := newSessionThatExitsLater(t, runtime, root)

		returned := make(chan error, 1)
		go func() {
			returned <- runtime.CloseSessionInBackground(t.Context(), root)
		}()
		synctest.Wait()
		select {
		case err := <-returned:
			require.NoError(t, err)
		default:
			t.Fatal("close waited for the old session to shut down")
		}

		newSession, err := runtime.getOrCreateSession(root)
		require.NoError(t, err)
		s.NotSame(oldSession, newSession)

		close(serverExited)
		require.NoError(t, runtime.Close(t.Context()))
	})
}

// TestRuntimeCloseWaitsForBackgroundClose proves that runtime shutdown returns only after a session that closes in
// the background has shut down.
func (s *backgroundCloseSuite) TestRuntimeCloseWaitsForBackgroundClose() {
	synctest.Test(s.T(), func(t *testing.T) {
		runtime := newTestRuntime(t, nil)
		root := t.TempDir()
		_, serverExited := newSessionThatExitsLater(t, runtime, root)
		go func() {
			assert.NoError(t, runtime.CloseSessionInBackground(t.Context(), root))
		}()
		synctest.Wait()

		closed := make(chan error, 1)
		go func() {
			closed <- runtime.Close(t.Context())
		}()
		synctest.Wait()
		s.Empty(closed, "runtime close returned while a session still shuts down")

		close(serverExited)
		require.NoError(t, <-closed)
	})
}

// newSessionThatExitsLater registers a session of root whose server process exits only when the returned channel
// closes, so its shutdown lasts until then.
func newSessionThatExitsLater(t *testing.T, runtime *Runtime, root string) (*session, chan struct{}) {
	t.Helper()

	oldSession, err := runtime.getOrCreateSession(root)
	require.NoError(t, err)
	serverExited := make(chan struct{})
	oldSession.done = serverExited

	return oldSession, serverExited
}
