package lspphpactor

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
)

// TestReferenceSearchConfigKeepsTargetOpenForWholeWorkflow proves that Phpactor references use the standard
// target-only open-file workflow so the target document stays registered through documentSymbol and references.
func TestReferenceSearchConfigKeepsTargetOpenForWholeWorkflow(t *testing.T) {
	t.Parallel()

	config := referenceSearchConfig(
		func(context.Context, string) (jsonrpc2.Conn, error) { return nil, errors.New("unused") },
		func(context.Context, jsonrpc2.Conn, string, func(context.Context) error) error { return nil },
	)
	require.NotNil(t, config)
	assert.NotNil(t, config.WithRequestDocument)
	assert.True(t, config.OpenFileForDocumentSymbol)
	assert.True(t, config.OpenFileForReferenceWorkflow)
	assert.Equal(t, extensions, config.Extensions)
}
