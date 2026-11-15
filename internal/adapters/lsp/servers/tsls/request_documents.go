package lsptsls

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
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

// warmReferenceWorkflowFiles revisits the open reference workflow files so tsls can resolve cross-file imports
// against a fully materialized in-memory project view, and returns the files that answered. A neighbor file that
// fails is returned as skipped; a failure of the target file, which comes last, or an error that ends the whole
// request is returned as error.
func warmReferenceWorkflowFiles(
	ctx context.Context,
	conn jsonrpc2.Conn,
	workspaceRoot string,
	relativePaths []string,
) ([]string, []helpers.SkippedFile, error) {
	warmedPaths := make([]string, 0, len(relativePaths))
	var skipped []helpers.SkippedFile
	for index, relativePath := range relativePaths {
		absolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(relativePath))
		if err := warmRequestDocument(ctx, conn, absolutePath); err != nil {
			if index == len(relativePaths)-1 || helpers.IsRequestError(ctx, err) {
				return nil, nil, err
			}
			skipped = append(skipped, helpers.SkippedFile{RelativePath: relativePath, Err: err})

			continue
		}
		warmedPaths = append(warmedPaths, relativePath)
	}

	return warmedPaths, skipped, nil
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
