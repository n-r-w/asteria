package helpers

import (
	"fmt"
	"strings"

	"go.lsp.dev/protocol"
)

// HoverText extracts the trimmed hover text from hover contents that carry one markup or marked-string value.
func HoverText(contents protocol.HoverContents) (string, error) {
	switch value := contents.(type) {
	case nil:
		return "", nil
	case *protocol.MarkupContent:
		return strings.TrimSpace(value.Value), nil
	//nolint:staticcheck // Language servers still send this deprecated shape, and its value is the hover text.
	case *protocol.MarkedStringWithLanguage:
		return strings.TrimSpace(value.Value), nil
	default:
		return "", fmt.Errorf("unsupported hover contents %T", contents)
	}
}
