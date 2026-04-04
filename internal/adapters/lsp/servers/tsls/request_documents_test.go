package lsptsls

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// TestNewWithRequestDocumentWarmsOpenedFileBeforeRun proves that the tsls-specific request-document
// wrapper waits for one documentSymbol round-trip before it runs the wrapped request logic.
func TestNewWithRequestDocumentWarmsOpenedFileBeforeRun(t *testing.T) {
	t.Parallel()

	fixturePath := writeWarmRequestDocumentFixture(t, "fixture.ts")
	conn, recorder := newWarmRequestDocumentConn(t)
	withRequestDocument := newWithRequestDocument()
	fixtureURI := uri.File(fixturePath)

	var methodsDuringRun []string
	err := withRequestDocument(t.Context(), conn, fixturePath, func(context.Context) error {
		methodsDuringRun = recorder.methodsForURI(fixtureURI)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{protocol.MethodTextDocumentDidOpen, protocol.MethodTextDocumentDocumentSymbol}, methodsDuringRun)
	waitForWarmRequestDocumentEvents(t, recorder, fixtureURI, []string{
		protocol.MethodTextDocumentDidOpen,
		protocol.MethodTextDocumentDocumentSymbol,
		protocol.MethodTextDocumentDidClose,
	})
}

// warmRequestDocumentEvent records one didOpen, documentSymbol, or didClose operation for later assertions.
type warmRequestDocumentEvent struct {
	Method string
	URI    uri.URI
}

// warmRequestDocumentRecorder keeps request-document event assertions deterministic across goroutines.
type warmRequestDocumentRecorder struct {
	mu      sync.Mutex
	records []warmRequestDocumentEvent
}

// addEvent appends one protocol event in arrival order.
func (r *warmRequestDocumentRecorder) addEvent(event warmRequestDocumentEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.records = append(r.records, event)
}

// methodsForURI returns the ordered methods recorded for one concrete URI.
func (r *warmRequestDocumentRecorder) methodsForURI(targetURI uri.URI) []string {
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

// waitForWarmRequestDocumentEvents waits until the recorder sees the exact method sequence for one URI.
func waitForWarmRequestDocumentEvents(
	t *testing.T,
	recorder *warmRequestDocumentRecorder,
	targetURI uri.URI,
	expected []string,
) {
	t.Helper()

	require.Eventually(t, func() bool {
		return slices.Equal(recorder.methodsForURI(targetURI), expected)
	}, time.Second, 10*time.Millisecond)
}

// writeWarmRequestDocumentFixture keeps the warm-up test focused on protocol order instead of file setup.
func writeWarmRequestDocumentFixture(t *testing.T, fileName string) string {
	t.Helper()

	workspaceRoot := t.TempDir()
	fixturePath := filepath.Join(workspaceRoot, fileName)
	require.NoError(t, os.WriteFile(fixturePath, []byte("export const fixture = 1;\n"), 0o600))

	return fixturePath
}

// newWarmRequestDocumentConn builds an in-memory JSON-RPC peer that records didOpen, documentSymbol,
// and didClose in the order that the tsls request-document wrapper issues them.
func newWarmRequestDocumentConn(t *testing.T) (jsonrpc2.Conn, *warmRequestDocumentRecorder) {
	t.Helper()

	serverSide, clientSide := net.Pipe()
	serverConn := jsonrpc2.NewConn(jsonrpc2.NewStream(serverSide))
	clientConn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	recorder := &warmRequestDocumentRecorder{}

	serverConn.Go(t.Context(), func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodTextDocumentDidOpen:
			var params protocol.DidOpenTextDocumentParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return err
			}
			recorder.addEvent(warmRequestDocumentEvent{Method: req.Method(), URI: params.TextDocument.URI})

			return nil
		case protocol.MethodTextDocumentDocumentSymbol:
			var params protocol.DocumentSymbolParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return reply(ctx, nil, err)
			}
			recorder.addEvent(warmRequestDocumentEvent{Method: req.Method(), URI: params.TextDocument.URI})

			return reply(ctx, []json.RawMessage{}, nil)
		case protocol.MethodTextDocumentDidClose:
			var params protocol.DidCloseTextDocumentParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return err
			}
			recorder.addEvent(warmRequestDocumentEvent{Method: req.Method(), URI: params.TextDocument.URI})

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
