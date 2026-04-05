package lsptsls

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// newWithRequestDocument keeps the shared didOpen/didClose lifecycle but adds one tsls-specific
// warm-up round-trip so freshly opened files are indexed before cross-file references run.
func newWithRequestDocument() stdlsp.WithRequestDocumentFunc {
	baseWithRequestDocument := helpers.WithRequestDocument(languageIDForExtension)

	return func(
		ctx context.Context,
		conn jsonrpc2.Conn,
		absolutePath string,
		run func(context.Context) error,
	) error {
		return baseWithRequestDocument(ctx, conn, absolutePath, func(callCtx context.Context) error {
			if err := warmRequestDocument(callCtx, conn, absolutePath); err != nil {
				return err
			}

			return run(callCtx)
		})
	}
}

// warmRequestDocuments revisits the full open-file set after every participant is open so tsls can resolve
// cross-file imports against a fully materialized in-memory project view.
func warmRequestDocuments(ctx context.Context, conn jsonrpc2.Conn, absolutePaths []string) error {
	for _, absolutePath := range absolutePaths {
		if err := warmRequestDocument(ctx, conn, absolutePath); err != nil {
			return err
		}
	}

	return nil
}

// warmRequestDocument forces tsls to acknowledge one opened file through documentSymbol before the
// adapter issues symbol or reference requests that depend on fresh indexing.
func warmRequestDocument(ctx context.Context, conn jsonrpc2.Conn, absolutePath string) error {
	cleanAbsolutePath := filepath.Clean(absolutePath)
	params := &protocol.DocumentSymbolParams{
		WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
		PartialResultParams:    protocol.PartialResultParams{},
		TextDocument: protocol.TextDocumentIdentifier{
			URI: uri.File(cleanAbsolutePath),
		},
	}

	var payload []json.RawMessage
	if err := protocol.Call(ctx, conn, protocol.MethodTextDocumentDocumentSymbol, params, &payload); err != nil {
		return fmt.Errorf("warm request document %q: %w", cleanAbsolutePath, err)
	}

	return nil
}
