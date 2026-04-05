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

// requestDocumentState keeps one request-scoped document lifecycle stable across repeated temporary opens.
type requestDocumentState struct {
	refCount int
	version  int32
	content  string
}

// syncRequestDocumentContent opens one request document and pushes a full-content change when a reopened file
// now contains different on-disk text than the last temporary request lifecycle saw.
func syncRequestDocumentContent(
	ctx context.Context,
	conn jsonrpc2.Conn,
	cleanAbsolutePath string,
	documentURI uri.URI,
	languageID string,
	documentState *requestDocumentState,
	resyncOnReopen bool,
) error {
	content, readErr := os.ReadFile(filepath.Clean(cleanAbsolutePath))
	if readErr != nil {
		return fmt.Errorf("read request document %q: %w", cleanAbsolutePath, readErr)
	}
	contentText := string(content)
	if !resyncOnReopen {
		openErr := conn.Notify(ctx, protocol.MethodTextDocumentDidOpen, &protocol.DidOpenTextDocumentParams{
			TextDocument: protocol.TextDocumentItem{
				URI:        documentURI,
				LanguageID: protocol.LanguageIdentifier(languageID),
				Version:    0,
				Text:       contentText,
			},
		})
		if openErr != nil {
			return fmt.Errorf("open request document %q: %w", cleanAbsolutePath, openErr)
		}

		return nil
	}

	nextVersion := documentState.version + 1

	openErr := conn.Notify(ctx, protocol.MethodTextDocumentDidOpen, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI:        documentURI,
			LanguageID: protocol.LanguageIdentifier(languageID),
			Version:    nextVersion,
			Text:       contentText,
		},
	})
	if openErr != nil {
		return fmt.Errorf("open request document %q: %w", cleanAbsolutePath, openErr)
	}

	if documentState.version > 0 && documentState.content != contentText {
		nextVersion++
		changeErr := conn.Notify(ctx, protocol.MethodTextDocumentDidChange, &protocol.DidChangeTextDocumentParams{
			TextDocument: protocol.VersionedTextDocumentIdentifier{
				TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: documentURI},
				Version:                nextVersion,
			},
			//nolint:exhaustruct // Full-content replacement intentionally omits optional range fields.
			ContentChanges: []protocol.TextDocumentContentChangeEvent{{Text: contentText}},
		})
		if changeErr != nil {
			return fmt.Errorf("change request document %q: %w", cleanAbsolutePath, changeErr)
		}
	}

	documentState.version = nextVersion
	documentState.content = contentText

	return nil
}

// WithRequestDocument opens one file for the duration of one request so adapters can share the same
// temporary didOpen/didClose workflow without duplicating lifecycle code.
func WithRequestDocument(languageID func(ext string) string) func(
	ctx context.Context,
	conn jsonrpc2.Conn,
	absolutePath string,
	run func(context.Context) error,
) error {
	return withRequestDocument(languageID, false)
}

// WithRequestDocumentResyncOnReopen replays a full-content didChange when a temporarily reopened file changed
// on disk since its previous request lifecycle on the same connection.
func WithRequestDocumentResyncOnReopen(languageID func(ext string) string) func(
	ctx context.Context,
	conn jsonrpc2.Conn,
	absolutePath string,
	run func(context.Context) error,
) error {
	return withRequestDocument(languageID, true)
}

// WithRequestDocument opens one file for the duration of one request so adapters can share the same
// temporary didOpen/didClose workflow without duplicating lifecycle code.
func withRequestDocument(languageID func(ext string) string, resyncOnReopen bool) func(
	ctx context.Context,
	conn jsonrpc2.Conn,
	absolutePath string,
	run func(context.Context) error,
) error {
	var mu sync.Mutex
	stateByConn := make(map[jsonrpc2.Conn]map[uri.URI]*requestDocumentState)

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
		sessionStateByURI, ok := stateByConn[conn]
		if !ok {
			sessionStateByURI = make(map[uri.URI]*requestDocumentState)
		}
		documentState, ok := sessionStateByURI[documentURI]
		if !ok {
			documentState = &requestDocumentState{}
			sessionStateByURI[documentURI] = documentState
		}
		if documentState.refCount == 0 {
			syncErr := syncRequestDocumentContent(
				ctx,
				conn,
				cleanAbsolutePath,
				documentURI,
				languageID(filepath.Ext(cleanAbsolutePath)),
				documentState,
				resyncOnReopen,
			)
			if syncErr != nil {
				mu.Unlock()

				return syncErr
			}
		}
		documentState.refCount++
		stateByConn[conn] = sessionStateByURI
		mu.Unlock()

		defer func() {
			mu.Lock()
			deferredStateByURI := stateByConn[conn]
			deferredDocumentState := deferredStateByURI[documentURI]
			deferredDocumentState.refCount--
			shouldClose := deferredDocumentState.refCount == 0
			shouldForgetState := shouldClose && !resyncOnReopen

			var closeErr error
			if shouldClose {
				closeErr = conn.Notify(
					context.WithoutCancel(ctx),
					protocol.MethodTextDocumentDidClose,
					&protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: documentURI}},
				)
			}
			if shouldForgetState {
				delete(deferredStateByURI, documentURI)
				if len(deferredStateByURI) == 0 {
					delete(stateByConn, conn)
				}
			}
			mu.Unlock()

			if closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close request document %q: %w", cleanAbsolutePath, closeErr))
			}
		}()

		return run(ctx)
	}
}
