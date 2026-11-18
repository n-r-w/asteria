package runtimelsp

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// TestClientHandlerDelegatesAdapterSpecificCallbacks proves that one adapter can intercept custom
// server-to-client methods before the generic runtime fallback rejects them.
func TestClientHandlerDelegatesAdapterSpecificCallbacks(t *testing.T) {
	t.Parallel()

	var handledMethods []string
	handler := newClientHandler(
		t.TempDir(),
		nil,
		nil,
		func(_ context.Context, req *jsonrpc2.Request, _ string) (any, bool, error) {
			handledMethods = append(handledMethods, strings.Clone(req.Method()))
			return []any{}, true, nil
		},
	)

	var result []any
	err := callHandler(t, handler, "workspace/executeClientCommand", struct{}{}, &result)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Empty(t, result)
	assert.Equal(t, []string{"workspace/executeClientCommand"}, handledMethods)
}

// TestClientHandlerFallsBackToDefaultReplies proves that adapter hooks do not break baseline generic callbacks.
func TestClientHandlerFallsBackToDefaultReplies(t *testing.T) {
	t.Parallel()

	handler := newClientHandler(
		t.TempDir(),
		nil,
		nil,
		func(context.Context, *jsonrpc2.Request, string) (any, bool, error) {
			return nil, false, nil
		},
	)

	var result json.RawMessage
	err := callHandler(t, handler, protocol.MethodClientRegisterCapability, struct{}{}, &result)
	require.NoError(t, err)
	assert.JSONEq(t, "null", string(result))
}

// callHandler sends one call to handler through an in-memory JSON-RPC connection and decodes its reply.
func callHandler(t *testing.T, handler jsonrpc2.Handler, method string, params, result any) error {
	t.Helper()

	handlerSide, callerSide := net.Pipe()
	handlerConn := jsonrpc2.NewConn(jsonrpc2.NewStream(handlerSide))
	callerConn := jsonrpc2.NewConn(jsonrpc2.NewStream(callerSide))
	handlerConn.Go(t.Context(), handler)
	callerConn.Go(t.Context(), jsonrpc2.MethodNotFoundHandler)
	t.Cleanup(func() {
		require.NoError(t, callerConn.Close())
		require.NoError(t, handlerConn.Close())
	})

	_, err := callerConn.Call(t.Context(), method, params, result)

	return err
}
