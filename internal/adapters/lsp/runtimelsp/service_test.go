package runtimelsp

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.lsp.dev/jsonrpc2"
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

		acquired, err := runtime.AcquireClosableSession(t.Context(), root)
		require.NoError(t, err)
		acquired.Release()
		returned := make(chan struct{})
		go func() {
			acquired.CloseInBackground(t.Context())
			close(returned)
		}()
		synctest.Wait()
		if !isClosed(returned) {
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
		closeAcquiredInBackground(t, runtime, root)
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

// TestLateCleanupKeepsReplacementSession proves that the cleanup of a request whose session was already detached
// leaves the replacement session that another request uses registered.
func (s *backgroundCloseSuite) TestLateCleanupKeepsReplacementSession() {
	synctest.Test(s.T(), func(t *testing.T) {
		runtime := newTestRuntime(t, nil)
		root := t.TempDir()
		_, oldExited := newSessionThatExitsLater(t, runtime, root)
		requestA, err := runtime.AcquireClosableSession(t.Context(), root)
		require.NoError(t, err)
		requestB, err := runtime.AcquireClosableSession(t.Context(), root)
		require.NoError(t, err)

		requestA.Release()
		requestA.CloseInBackground(t.Context())
		releaseC, err := runtime.AcquireSession(t.Context(), root)
		require.NoError(t, err)
		replacement, err := runtime.getOrCreateSession(root)
		require.NoError(t, err)

		requestB.Release()
		requestB.CloseInBackground(t.Context())

		registered, err := runtime.getOrCreateSession(root)
		require.NoError(t, err)
		s.Same(replacement, registered)

		releaseC()
		close(oldExited)
		require.NoError(t, runtime.Close(t.Context()))
	})
}

// TestRuntimeCloseForcesBackgroundCloseToStop proves that runtime shutdown ends the graceful phase of a session that
// closes in the background at once, waits until its server exits, and closes the live sessions too.
// It runs in real time because the JSON-RPC connection reuses its call state across synctest bubbles.
func (s *backgroundCloseSuite) TestRuntimeCloseForcesBackgroundCloseToStop() {
	t := s.T()
	const shutdownTimeout = 5 * time.Second
	runtime := newTestRuntime(t, func(config *LSPConfig) { config.ShutdownTimeout = shutdownTimeout })
	detachedRoot := t.TempDir()
	detachedExited := newSessionThatIgnoresShutdown(t, runtime, detachedRoot)
	liveSession, liveExited := newSessionThatExitsLater(t, runtime, t.TempDir())
	close(liveExited)
	closeAcquiredInBackground(t, runtime, detachedRoot)

	start := time.Now()
	s.Require().NoError(runtime.Close(t.Context()))

	s.Less(time.Since(start), time.Second)
	s.True(isClosed(detachedExited))
	s.Zero(runtime.sessionCount())
	s.Nil(liveSession.done)
}

// newSessionThatIgnoresShutdown registers a session of root whose server never answers the shutdown request and
// exits when the session closes its side of the connection; the returned channel closes when the server exits.
func newSessionThatIgnoresShutdown(t *testing.T, runtime *Runtime, root string) chan struct{} {
	t.Helper()

	detachedSession, err := runtime.getOrCreateSession(root)
	require.NoError(t, err)
	serverSide, clientSide := net.Pipe()
	serverConn := jsonrpc2.NewConn(jsonrpc2.NewStream(serverSide))
	clientConn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	// The server keeps reading its input while the shutdown request waits for an answer that never comes.
	serverConn.Go(t.Context(), jsonrpc2.AsyncHandler(func(ctx context.Context, req *jsonrpc2.Request) (any, error) {
		if !req.IsCall() {
			//nolint:nilnil // A notification gets no reply.
			return nil, nil
		}
		<-ctx.Done()

		return nil, ctx.Err()
	}))
	clientConn.Go(t.Context(), jsonrpc2.MethodNotFoundHandler)
	exited := make(chan struct{})
	go func() {
		<-clientConn.Done()
		close(exited)
	}()
	detachedSession.conn = clientConn
	detachedSession.done = exited
	t.Cleanup(func() { _ = serverConn.Close() })

	return exited
}

// isClosed reports whether ch is closed.
func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// closeAcquiredInBackground acquires the session of root, releases it, and closes it in the background, as a request
// that ended by its deadline does.
func closeAcquiredInBackground(t *testing.T, runtime *Runtime, root string) {
	t.Helper()

	acquired, err := runtime.AcquireClosableSession(t.Context(), root)
	require.NoError(t, err)
	acquired.Release()
	acquired.CloseInBackground(t.Context())
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
