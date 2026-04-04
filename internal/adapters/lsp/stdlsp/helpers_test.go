package stdlsp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// mustRawMessages marshals test symbols into the raw JSON shape returned by LSP documentSymbol requests.
func mustRawMessages[T any](t *testing.T, symbols []T) []json.RawMessage {
	t.Helper()

	rawMessages := make([]json.RawMessage, 0, len(symbols))
	for _, symbol := range symbols {
		payload, err := json.Marshal(symbol)
		require.NoError(t, err)
		rawMessages = append(rawMessages, json.RawMessage(payload))
	}

	return rawMessages
}
