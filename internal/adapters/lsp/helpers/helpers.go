// Package helpers keeps adapter helpers shared across packages.
package helpers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/n-r-w/asteria/internal/domain"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// FindOverviewSymbol keeps overview lookups consistent across adapter tests.
func FindOverviewSymbol(symbols []domain.SymbolLocation, path string) (domain.SymbolLocation, bool) {
	for _, symbol := range symbols {
		if symbol.Path == path {
			return symbol, true
		}
	}

	return domain.SymbolLocation{}, false
}

// WithRequestDocument opens one file for the duration of one request so adapters can share the same
// temporary didOpen/didClose workflow without duplicating lifecycle code.
func WithRequestDocument(languageID func(ext string) string) func(
	ctx context.Context,
	conn jsonrpc2.Conn,
	absolutePath string,
	run func(context.Context) error,
) error {
	var mu sync.Mutex
	refCountByConn := make(map[jsonrpc2.Conn]map[uri.URI]int)

	return func(
		ctx context.Context,
		conn jsonrpc2.Conn,
		absolutePath string,
		run func(context.Context) error,
	) (err error) {
		cleanAbsolutePath := filepath.Clean(absolutePath)
		documentURI := uri.File(cleanAbsolutePath)

		// The lock stays held across didOpen and didClose transitions so concurrent callers for the
		// same connection and URI cannot race ahead of a lifecycle change the language server has not
		// processed yet.
		mu.Lock()
		sessionRefCountByURI, ok := refCountByConn[conn]
		if !ok {
			sessionRefCountByURI = make(map[uri.URI]int)
		}
		if sessionRefCountByURI[documentURI] == 0 {
			content, readErr := os.ReadFile(cleanAbsolutePath)
			if readErr != nil {
				mu.Unlock()

				return fmt.Errorf("read request document %q: %w", cleanAbsolutePath, readErr)
			}

			openErr := conn.Notify(ctx, protocol.MethodTextDocumentDidOpen, &protocol.DidOpenTextDocumentParams{
				TextDocument: protocol.TextDocumentItem{
					URI:        documentURI,
					LanguageID: protocol.LanguageIdentifier(languageID(filepath.Ext(cleanAbsolutePath))),
					Version:    0,
					Text:       string(content),
				},
			})
			if openErr != nil {
				mu.Unlock()

				return fmt.Errorf("open request document %q: %w", cleanAbsolutePath, openErr)
			}
		}
		sessionRefCountByURI[documentURI]++
		refCountByConn[conn] = sessionRefCountByURI
		mu.Unlock()

		defer func() {
			mu.Lock()
			deferredRefCountByURI := refCountByConn[conn]
			deferredRefCountByURI[documentURI]--
			shouldClose := deferredRefCountByURI[documentURI] == 0
			if shouldClose {
				delete(deferredRefCountByURI, documentURI)
				if len(deferredRefCountByURI) == 0 {
					delete(refCountByConn, conn)
				}
			}

			var closeErr error
			if shouldClose {
				closeErr = conn.Notify(
					context.WithoutCancel(ctx),
					protocol.MethodTextDocumentDidClose,
					&protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: documentURI}},
				)
			}
			mu.Unlock()

			if closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close request document %q: %w", cleanAbsolutePath, closeErr))
			}
		}()

		return run(ctx)
	}
}
