package runtimelsp

import (
	"context"
	"encoding/json"
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

	handledMethods := make([]string, 0, 1)
	handler := newClientHandler(
		t.TempDir(),
		nil,
		nil,
		func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request, workspaceRoot string) (bool, error) {
			handledMethods = append(handledMethods, req.Method())
			return true, reply(ctx, []any{}, nil)
		},
	)

	request := decodeCall(t, `{"jsonrpc":"2.0","id":1,"method":"workspace/executeClientCommand","params":{}}`)
	var repliedResult []any
	replied := false
	err := handler(
		t.Context(),
		func(ctx context.Context, result any, err error) error {
			require.NoError(t, err)
			require.IsType(t, []any{}, result)
			repliedResult = result.([]any)
			replied = true

			return nil
		},
		request,
	)
	require.NoError(t, err)
	assert.True(t, replied)
	assert.Empty(t, repliedResult)
	assert.Equal(t, []string{"workspace/executeClientCommand"}, handledMethods)
}

// TestClientHandlerFallsBackToDefaultReplies proves that adapter hooks do not break baseline generic callbacks.
func TestClientHandlerFallsBackToDefaultReplies(t *testing.T) {
	t.Parallel()

	handler := newClientHandler(
		t.TempDir(),
		nil,
		nil,
		func(context.Context, jsonrpc2.Replier, jsonrpc2.Request, string) (bool, error) {
			return false, nil
		},
	)

	request := decodeNotification(t, `{"jsonrpc":"2.0","method":"`+protocol.MethodWindowLogMessage+`","params":{"type":3,"message":"ready"}}`)
	replied := false
	err := handler(
		t.Context(),
		func(context.Context, any, error) error {
			replied = true
			return nil
		},
		request,
	)
	require.NoError(t, err)
	assert.True(t, replied)
}

// decodeCall keeps jsonrpc2 request construction explicit without depending on unexported constructors.
func decodeCall(t *testing.T, raw string) jsonrpc2.Request {
	t.Helper()

	var request jsonrpc2.Call
	require.NoError(t, json.Unmarshal([]byte(raw), &request))

	return &request
}

// decodeNotification keeps notification-based handler tests short and readable.
func decodeNotification(t *testing.T, raw string) jsonrpc2.Request {
	t.Helper()

	var request jsonrpc2.Notification
	require.NoError(t, json.Unmarshal([]byte(raw), &request))

	return &request
}
