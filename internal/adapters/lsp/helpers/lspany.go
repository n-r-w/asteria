package helpers

import (
	"encoding/json"
	"fmt"

	"go.lsp.dev/protocol"
)

// MarshalLSPAny encodes one Go value as the raw JSON form that protocol fields of type LSPAny carry.
func MarshalLSPAny(value any) (protocol.LSPAny, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode LSP value: %w", err)
	}

	return protocol.LSPAny(encoded), nil
}
