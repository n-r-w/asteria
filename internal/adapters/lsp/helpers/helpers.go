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
	isOpen   bool
}

type requestDocumentMode struct {
	resyncOnReopen bool
	keepOpen       bool
	reopenOnChange bool
}

func openRequestDocument(
	ctx context.Context,
	conn jsonrpc2.Conn,
	documentURI uri.URI,
	languageID string,
	version int32,
	contentText string,
) error {
	return conn.Notify(ctx, protocol.MethodTextDocumentDidOpen, &protocol.DidOpenTextDocumentParams{
		TextDocument: protocol.TextDocumentItem{
			URI:        documentURI,
			LanguageID: protocol.LanguageIdentifier(languageID),
			Version:    version,
			Text:       contentText,
		},
	})
}

func changeRequestDocument(
	ctx context.Context,
	conn jsonrpc2.Conn,
	documentURI uri.URI,
	version int32,
	contentText string,
) error {
	return conn.Notify(ctx, protocol.MethodTextDocumentDidChange, &protocol.DidChangeTextDocumentParams{
		TextDocument: protocol.VersionedTextDocumentIdentifier{
			TextDocumentIdentifier: protocol.TextDocumentIdentifier{URI: documentURI},
			Version:                version,
		},
		//nolint:exhaustruct // Full-content replacement intentionally omits optional range fields.
		ContentChanges: []protocol.TextDocumentContentChangeEvent{{Text: contentText}},
	})
}

func closeRequestDocument(ctx context.Context, conn jsonrpc2.Conn, documentURI uri.URI) error {
	return conn.Notify(ctx, protocol.MethodTextDocumentDidClose, &protocol.DidCloseTextDocumentParams{
		TextDocument: protocol.TextDocumentIdentifier{URI: documentURI},
	})
}

func syncPlainRequestDocument(
	ctx context.Context,
	conn jsonrpc2.Conn,
	cleanAbsolutePath string,
	documentURI uri.URI,
	languageID string,
	documentState *requestDocumentState,
	contentText string,
) error {
	if err := openRequestDocument(ctx, conn, documentURI, languageID, 0, contentText); err != nil {
		return fmt.Errorf("open request document %q: %w", cleanAbsolutePath, err)
	}

	documentState.isOpen = true

	return nil
}

func syncPersistentRequestDocument(
	ctx context.Context,
	conn jsonrpc2.Conn,
	cleanAbsolutePath string,
	documentURI uri.URI,
	languageID string,
	documentState *requestDocumentState,
	contentText string,
	mode requestDocumentMode,
) error {
	if !documentState.isOpen {
		nextVersion := documentState.version + 1
		if err := openRequestDocument(ctx, conn, documentURI, languageID, nextVersion, contentText); err != nil {
			return fmt.Errorf("open request document %q: %w", cleanAbsolutePath, err)
		}

		documentState.isOpen = true
		documentState.version = nextVersion
		documentState.content = contentText

		return nil
	}
	if mode.reopenOnChange && documentState.content != contentText {
		if err := closeRequestDocument(ctx, conn, documentURI); err != nil {
			return fmt.Errorf("close request document %q: %w", cleanAbsolutePath, err)
		}
		documentState.isOpen = false

		nextVersion := documentState.version + 1
		if err := openRequestDocument(ctx, conn, documentURI, languageID, nextVersion, contentText); err != nil {
			return fmt.Errorf("open request document %q: %w", cleanAbsolutePath, err)
		}

		documentState.isOpen = true
		documentState.version = nextVersion
		documentState.content = contentText

		return nil
	}
	if documentState.content == contentText {
		return nil
	}

	nextVersion := documentState.version + 1
	if err := changeRequestDocument(ctx, conn, documentURI, nextVersion, contentText); err != nil {
		return fmt.Errorf("change request document %q: %w", cleanAbsolutePath, err)
	}

	documentState.version = nextVersion
	documentState.content = contentText

	return nil
}

func syncReopenedRequestDocument(
	ctx context.Context,
	conn jsonrpc2.Conn,
	cleanAbsolutePath string,
	documentURI uri.URI,
	languageID string,
	documentState *requestDocumentState,
	contentText string,
) error {
	nextVersion := documentState.version + 1
	if err := openRequestDocument(ctx, conn, documentURI, languageID, nextVersion, contentText); err != nil {
		return fmt.Errorf("open request document %q: %w", cleanAbsolutePath, err)
	}
	documentState.isOpen = true

	if documentState.version > 0 && documentState.content != contentText {
		nextVersion++
		if err := changeRequestDocument(ctx, conn, documentURI, nextVersion, contentText); err != nil {
			return fmt.Errorf("change request document %q: %w", cleanAbsolutePath, err)
		}
	}

	documentState.version = nextVersion
	documentState.content = contentText

	return nil
}

// syncRequestDocumentContent synchronizes one request document with the current on-disk content according to
// the chosen lifecycle mode.
func syncRequestDocumentContent(
	ctx context.Context,
	conn jsonrpc2.Conn,
	cleanAbsolutePath string,
	documentURI uri.URI,
	languageID string,
	documentState *requestDocumentState,
	mode requestDocumentMode,
) error {
	content, readErr := os.ReadFile(filepath.Clean(cleanAbsolutePath))
	if readErr != nil {
		return fmt.Errorf("read request document %q: %w", cleanAbsolutePath, readErr)
	}
	contentText := string(content)
	if mode.keepOpen {
		return syncPersistentRequestDocument(
			ctx,
			conn,
			cleanAbsolutePath,
			documentURI,
			languageID,
			documentState,
			contentText,
			mode,
		)
	}
	if mode.resyncOnReopen {
		return syncReopenedRequestDocument(
			ctx,
			conn,
			cleanAbsolutePath,
			documentURI,
			languageID,
			documentState,
			contentText,
		)
	}

	return syncPlainRequestDocument(
		ctx,
		conn,
		cleanAbsolutePath,
		documentURI,
		languageID,
		documentState,
		contentText,
	)
}

// WithRequestDocument opens one file for the duration of one request so adapters can share the same
// temporary didOpen/didClose workflow without duplicating lifecycle code.
func WithRequestDocument(languageID func(ext string) string) func(
	ctx context.Context,
	conn jsonrpc2.Conn,
	absolutePath string,
	run func(context.Context) error,
) error {
	return withRequestDocument(languageID, requestDocumentMode{})
}

// WithRequestDocumentResyncOnReopen replays a full-content didChange when a temporarily reopened file changed
// on disk since its previous request lifecycle on the same connection.
func WithRequestDocumentResyncOnReopen(languageID func(ext string) string) func(
	ctx context.Context,
	conn jsonrpc2.Conn,
	absolutePath string,
	run func(context.Context) error,
) error {
	return withRequestDocument(languageID, requestDocumentMode{
		resyncOnReopen: true,
		keepOpen:       false,
		reopenOnChange: false,
	})
}

// WithPersistentRequestDocument keeps a request document open across request boundaries and refreshes the
// language-server buffer with a full-content didChange at the start of each later request on the same connection.
func WithPersistentRequestDocument(languageID func(ext string) string) func(
	ctx context.Context,
	conn jsonrpc2.Conn,
	absolutePath string,
	run func(context.Context) error,
) error {
	return withRequestDocument(languageID, requestDocumentMode{
		resyncOnReopen: true,
		keepOpen:       true,
		reopenOnChange: false,
	})
}

// WithPersistentRequestDocumentReopenOnChange keeps a request document open across requests, but when the
// on-disk file changed it refreshes the server state through didClose followed by a fresh didOpen.
func WithPersistentRequestDocumentReopenOnChange(languageID func(ext string) string) func(
	ctx context.Context,
	conn jsonrpc2.Conn,
	absolutePath string,
	run func(context.Context) error,
) error {
	return withRequestDocument(languageID, requestDocumentMode{resyncOnReopen: true, keepOpen: true, reopenOnChange: true})
}

// WithRequestDocument opens one file for the duration of one request so adapters can share the same
// temporary didOpen/didClose workflow without duplicating lifecycle code.
func withRequestDocument(languageID func(ext string) string, mode requestDocumentMode) func(
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
				mode,
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
			shouldClose := deferredDocumentState.refCount == 0 && !mode.keepOpen
			shouldForgetState := deferredDocumentState.refCount == 0 && !mode.resyncOnReopen && !mode.keepOpen

			var closeErr error
			if shouldClose {
				closeErr = conn.Notify(
					context.WithoutCancel(ctx),
					protocol.MethodTextDocumentDidClose,
					&protocol.DidCloseTextDocumentParams{TextDocument: protocol.TextDocumentIdentifier{URI: documentURI}},
				)
				deferredDocumentState.isOpen = false
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
