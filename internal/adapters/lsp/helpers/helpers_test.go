package helpers

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// TestWithRequestDocumentOpensAndClosesOneRequest proves that one plain request still emits exactly one
// didOpen and one didClose for the requested URI.
func TestWithRequestDocumentOpensAndClosesOneRequest(t *testing.T) {
	t.Parallel()

	fixturePath := writeRequestDocumentFixture(t, "fixture.ts")
	conn, recorder := newLifecycleRecorderConn(t)
	withRequestDocument := WithRequestDocument(func(_ string) string { return "typescript" })

	err := withRequestDocument(t.Context(), conn, fixturePath, func(context.Context) error {
		return nil
	})
	require.NoError(t, err)
	waitForURIMethods(t, recorder, uri.File(fixturePath), []string{"textDocument/didOpen", "textDocument/didClose"})
}

// TestWithRequestDocumentKeepsOneOpenForNestedSameURI proves that nested calls for one URI reuse the
// already opened document instead of sending a duplicate didOpen or premature didClose.
func TestWithRequestDocumentKeepsOneOpenForNestedSameURI(t *testing.T) {
	t.Parallel()

	fixturePath := writeRequestDocumentFixture(t, "fixture.ts")
	conn, recorder := newLifecycleRecorderConn(t)
	withRequestDocument := WithRequestDocument(func(_ string) string { return "typescript" })

	err := withRequestDocument(t.Context(), conn, fixturePath, func(callCtx context.Context) error {
		return withRequestDocument(callCtx, conn, fixturePath, func(context.Context) error {
			return nil
		})
	})
	require.NoError(t, err)
	waitForURIMethods(t, recorder, uri.File(fixturePath), []string{"textDocument/didOpen", "textDocument/didClose"})
}

// TestWithRequestDocumentTracksNestedDifferentURIs proves that nested calls for different files keep
// independent didOpen and didClose pairs for each URI.
func TestWithRequestDocumentTracksNestedDifferentURIs(t *testing.T) {
	t.Parallel()

	outerPath := writeRequestDocumentFixture(t, "fixture.ts")
	innerPath := writeRequestDocumentFixture(t, "references.ts")
	conn, recorder := newLifecycleRecorderConn(t)
	withRequestDocument := WithRequestDocument(func(_ string) string { return "typescript" })

	err := withRequestDocument(t.Context(), conn, outerPath, func(callCtx context.Context) error {
		return withRequestDocument(callCtx, conn, innerPath, func(context.Context) error {
			return nil
		})
	})
	require.NoError(t, err)
	waitForEvents(t, recorder, []documentLifecycleEvent{
		{Method: protocol.MethodTextDocumentDidOpen, URI: uri.File(outerPath)},
		{Method: protocol.MethodTextDocumentDidOpen, URI: uri.File(innerPath)},
		{Method: protocol.MethodTextDocumentDidClose, URI: uri.File(innerPath)},
		{Method: protocol.MethodTextDocumentDidClose, URI: uri.File(outerPath)},
	})
}

// TestWithRequestDocumentClosesAfterRunError proves that cleanup still closes the temporary document
// when the wrapped request itself fails.
func TestWithRequestDocumentClosesAfterRunError(t *testing.T) {
	t.Parallel()

	fixturePath := writeRequestDocumentFixture(t, "fixture.ts")
	conn, recorder := newLifecycleRecorderConn(t)
	withRequestDocument := WithRequestDocument(func(_ string) string { return "typescript" })
	expectedErr := errors.New("request failed")

	err := withRequestDocument(t.Context(), conn, fixturePath, func(context.Context) error {
		return expectedErr
	})
	require.ErrorIs(t, err, expectedErr)
	waitForURIMethods(t, recorder, uri.File(fixturePath), []string{"textDocument/didOpen", "textDocument/didClose"})
}

// TestWithRequestDocumentKeepsOneOpenForConcurrentSameURI proves that overlapping callers for the same
// file share one lifecycle state and close the document only after the last caller exits.
func TestWithRequestDocumentKeepsOneOpenForConcurrentSameURI(t *testing.T) {
	t.Parallel()

	fixturePath := writeRequestDocumentFixture(t, "fixture.ts")
	conn, recorder := newLifecycleRecorderConn(t)
	withRequestDocument := WithRequestDocument(func(_ string) string { return "typescript" })

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var enteredFirst atomic.Bool
	var enteredSecond atomic.Bool
	errCh := make(chan error, 2)

	go func() {
		errCh <- withRequestDocument(t.Context(), conn, fixturePath, func(context.Context) error {
			enteredFirst.Store(true)
			close(firstStarted)
			<-releaseFirst

			return nil
		})
	}()

	waitForSignal(t, firstStarted, "firstStarted")

	go func() {
		errCh <- withRequestDocument(t.Context(), conn, fixturePath, func(context.Context) error {
			enteredSecond.Store(true)
			close(secondStarted)

			return nil
		})
	}()

	waitForSignal(t, secondStarted, "secondStarted")
	close(releaseFirst)

	for range 2 {
		require.NoError(t, <-errCh)
	}

	assert.True(t, enteredFirst.Load())
	assert.True(t, enteredSecond.Load())
	waitForURIMethods(t, recorder, uri.File(fixturePath), []string{"textDocument/didOpen", "textDocument/didClose"})
}

// TestWithRequestDocumentKeepsSameURISeparateAcrossConnections proves that one shared helper still
// emits separate didOpen and didClose pairs when the same file is used on two different sessions.
func TestWithRequestDocumentKeepsSameURISeparateAcrossConnections(t *testing.T) {
	t.Parallel()

	fixturePath := writeRequestDocumentFixture(t, "fixture.ts")
	firstConn, firstRecorder := newLifecycleRecorderConn(t)
	secondConn, secondRecorder := newLifecycleRecorderConn(t)
	withRequestDocument := WithRequestDocument(func(_ string) string { return "typescript" })

	err := withRequestDocument(t.Context(), firstConn, fixturePath, func(callCtx context.Context) error {
		return withRequestDocument(callCtx, secondConn, fixturePath, func(context.Context) error {
			return nil
		})
	})
	require.NoError(t, err)
	waitForURIMethods(t, firstRecorder, uri.File(fixturePath), []string{"textDocument/didOpen", "textDocument/didClose"})
	waitForURIMethods(t, secondRecorder, uri.File(fixturePath), []string{"textDocument/didOpen", "textDocument/didClose"})
}

// waitForURIMethods waits until the recorder sees the exact lifecycle sequence for one URI.
func waitForURIMethods(t *testing.T, recorder *lifecycleRecorder, targetURI uri.URI, expected []string) {
	t.Helper()

	require.Eventually(t, func() bool {
		return slices.Equal(recorder.methodsForURI(targetURI), expected)
	}, time.Second, 10*time.Millisecond)
}

// waitForEvents waits until the recorder sees the exact full event stream.
func waitForEvents(t *testing.T, recorder *lifecycleRecorder, expected []documentLifecycleEvent) {
	t.Helper()

	require.Eventually(t, func() bool {
		return slices.Equal(recorder.events(), expected)
	}, time.Second, 10*time.Millisecond)
}

// waitForSignal fails fast when a test coordination channel never fires.
func waitForSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

// waitForConnDone keeps shutdown regressions local to the exact cleanup wait site.
func waitForConnDone(t *testing.T, name string, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s shutdown", name)
	}
}

// writeRequestDocumentFixture keeps lifecycle tests focused on protocol behavior instead of fixture setup.
func writeRequestDocumentFixture(t *testing.T, fileName string) string {
	t.Helper()

	workspaceRoot := t.TempDir()
	fixturePath := filepath.Join(workspaceRoot, fileName)
	require.NoError(t, os.WriteFile(fixturePath, []byte("export const fixture = 1;\n"), 0o600))

	return fixturePath
}

// documentLifecycleEvent records one didOpen or didClose notification for later assertions.
type documentLifecycleEvent struct {
	Method string
	URI    uri.URI
}

// lifecycleRecorder keeps JSON-RPC notification assertions deterministic across the recorder goroutine
// and the test goroutine.
type lifecycleRecorder struct {
	mu      sync.Mutex
	records []documentLifecycleEvent
}

// addEvent appends one protocol event in arrival order.
func (r *lifecycleRecorder) addEvent(event documentLifecycleEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.records = append(r.records, event)
}

// events returns a stable snapshot of all recorded events.
func (r *lifecycleRecorder) events() []documentLifecycleEvent {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]documentLifecycleEvent(nil), r.records...)
}

// methodsForURI extracts the ordered methods recorded for one concrete URI.
func (r *lifecycleRecorder) methodsForURI(targetURI uri.URI) []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	methods := make([]string, 0, len(r.records))
	for _, event := range r.records {
		if event.URI == targetURI {
			methods = append(methods, event.Method)
		}
	}

	return methods
}

// newLifecycleRecorderConn builds an in-memory JSON-RPC peer that records didOpen and didClose notifications.
func newLifecycleRecorderConn(t *testing.T) (jsonrpc2.Conn, *lifecycleRecorder) {
	t.Helper()

	serverSide, clientSide := net.Pipe()
	serverConn := jsonrpc2.NewConn(jsonrpc2.NewStream(serverSide))
	clientConn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	recorder := &lifecycleRecorder{}

	serverConn.Go(t.Context(), func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodTextDocumentDidOpen:
			var params protocol.DidOpenTextDocumentParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return err
			}
			recorder.addEvent(documentLifecycleEvent{Method: req.Method(), URI: params.TextDocument.URI})

			return nil
		case protocol.MethodTextDocumentDidClose:
			var params protocol.DidCloseTextDocumentParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return err
			}
			recorder.addEvent(documentLifecycleEvent{Method: req.Method(), URI: params.TextDocument.URI})

			return nil
		default:
			return jsonrpc2.MethodNotFoundHandler(ctx, reply, req)
		}
	})
	clientConn.Go(t.Context(), jsonrpc2.MethodNotFoundHandler)

	t.Cleanup(func() {
		require.NoError(t, clientConn.Close())
		require.NoError(t, serverConn.Close())
		waitForConnDone(t, "clientConn", clientConn.Done())
		waitForConnDone(t, "serverConn", serverConn.Done())
	})

	return clientConn, recorder
}
