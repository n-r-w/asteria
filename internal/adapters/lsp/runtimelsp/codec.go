package runtimelsp

import (
	"slices"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// lspCodec encodes LSP payloads like the unexported codec that protocol.NewClient installs, which replaces invalid
// UTF-8 in strings with U+FFFD, so that a file with binary string literals can still be opened on the server.
type lspCodec struct{}

var _ jsonrpc2.Codec = lspCodec{}

// Marshal encodes one request, notification, or reply payload and passes already-encoded raw messages through.
func (lspCodec) Marshal(v any) ([]byte, error) {
	switch message := v.(type) {
	case jsonrpc2.RawMessage:
		if message == nil {
			return []byte("null"), nil
		}

		return message, nil
	case *jsonrpc2.RawMessage:
		if message == nil || *message == nil {
			return []byte("null"), nil
		}

		return *message, nil
	}

	return protocol.Marshal(v)
}

// Unmarshal decodes one payload into v and copies it verbatim when v is a raw message.
func (lspCodec) Unmarshal(data []byte, v any) error {
	if message, ok := v.(*jsonrpc2.RawMessage); ok {
		*message = slices.Clone(data)

		return nil
	}

	return protocol.Unmarshal(data, v)
}
