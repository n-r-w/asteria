package lspphpactor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/server"
	"github.com/n-r-w/asteria/internal/usecase/router"
)

const phpactorShutdownTimeout = 15 * time.Second

// Service implements phpactor-specific symbolic search logic.
type Service struct {
	rt *runtimelsp.Runtime
	// ensureConn returns the live connection of the workspace session for adapter-specific requests.
	ensureConn          stdlsp.EnsureConnFunc
	std                 *stdlsp.Service
	withRequestDocument stdlsp.WithRequestDocumentFunc
	// runPropertyReferences returns the property usages that phpactor's member-reference command finds in the index at
	// indexPath.
	runPropertyReferences func(
		ctx context.Context,
		workspaceRoot string,
		indexPath string,
		referenceTarget string,
		propertyName string,
	) ([]phpactorReferenceRow, error)
	cacheRoot string
	// mu guards indexing.
	mu sync.Mutex
	// indexing holds the indexing signal of the current phpactor session of each workspace root.
	indexing map[string]indexingDone
}

// indexingDone closes when a phpactor session reports the end of indexing, so reference searches can wait for it.
type indexingDone chan struct{}

// isClosed reports whether the session has reported the end of indexing.
func (d indexingDone) isClosed() bool {
	select {
	case <-d:
		return true
	default:
		return false
	}
}

var (
	_ router.ILSP = (*Service)(nil)
	_ server.ILSP = (*Service)(nil)
)

// New creates a service that lazily starts phpactor on the first request while keeping adapter state inside
// the managed cache root instead of the analyzed workspace.
func New(cacheRoot string, sessionIdleTimeout time.Duration) (*Service, error) {
	normalizedCacheRoot, err := helpers.ResolveCacheRoot(cacheRoot)
	if err != nil {
		return nil, err
	}

	withRequestDocument := helpers.WithRequestDocument(func(_ string) string { return phpLanguageID })
	service := &Service{
		rt:                    nil,
		ensureConn:            nil,
		std:                   nil,
		withRequestDocument:   nil,
		runPropertyReferences: runPHPActorPropertyReferences,
		cacheRoot:             normalizedCacheRoot,
		mu:                    sync.Mutex{},
		indexing:              make(map[string]indexingDone),
	}

	rt, err := runtimelsp.New(&runtimelsp.RuntimeConfig{
		Command:                 phpactorServerName,
		Args:                    []string{"language-server"},
		ServerName:              phpactorServerName,
		ShutdownTimeout:         phpactorShutdownTimeout,
		ReplyConfiguration:      nil,
		BuildClientCapabilities: buildClientCapabilities,
		FileWatch: &runtimelsp.FileWatchConfig{
			RelevantFile: shouldWatchPHPFile,
			IgnoreDir:    shouldIgnoreDir,
		},
		PatchInitializeParams: service.patchInitializeParams,
		HandleServerCallback:  service.handleServerCallback,
		AfterInitialized:      service.ensureIndexerPathReady,
		WaitUntilReady:        nil,
		BuildWorkspaceFolders: nil,
		SessionIdleTimeout:    sessionIdleTimeout,
	})
	if err != nil {
		return nil, err
	}

	std, err := stdlsp.New(&stdlsp.Config{
		Extensions:                   extensions,
		EnsureConn:                   rt.EnsureConn,
		WithRequestDocument:          withRequestDocument,
		OpenFileForDocumentSymbol:    true,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    shouldIgnoreDir,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	})
	if err != nil {
		return nil, err
	}

	service.rt = rt
	service.ensureConn = rt.EnsureConn
	service.std = std
	service.withRequestDocument = withRequestDocument

	return service, nil
}

// buildClientCapabilities advertises the extra hover and reference features that phpactor needs for stable
// symbol info and member-reference responses, while reusing the shared runtime baseline elsewhere.
func buildClientCapabilities() protocol.ClientCapabilities {
	capabilities := runtimelsp.DefaultClientCapabilities()
	if capabilities.TextDocument == nil {
		capabilities.TextDocument = &protocol.TextDocumentClientCapabilities{}
	}
	capabilities.TextDocument.Hover = &protocol.HoverClientCapabilities{
		DynamicRegistration: new(true),
		ContentFormat:       []protocol.MarkupKind{protocol.MarkupKindMarkdown, protocol.MarkupKindPlainText},
	}
	capabilities.TextDocument.References = &protocol.ReferenceClientCapabilities{
		DynamicRegistration: new(true),
	}

	return capabilities
}

// shouldWatchPHPFile keeps runtime-managed workspace watching scoped to PHP source files.
func shouldWatchPHPFile(relativePath string) bool {
	for _, extension := range extensions {
		if strings.EqualFold(filepath.Ext(relativePath), extension) {
			return true
		}
	}

	return false
}

// ensureIndexerPathReady prepares the adapter-local Phpactor index directory before reference requests need it.
func (s *Service) ensureIndexerPathReady(_ context.Context, _ jsonrpc2.Conn, workspaceRoot string) error {
	return ensureIndexerPathExists(s.cacheRoot, workspaceRoot)
}

// handleServerCallback records the end of phpactor indexing for workspaceRoot and leaves each message to the default
// handling of the runtime.
func (s *Service) handleServerCallback(
	_ context.Context,
	req *jsonrpc2.Request,
	workspaceRoot string,
) (result any, handled bool, err error) {
	if req.Method() != protocol.MethodWindowShowMessage {
		return nil, false, nil
	}

	var params protocol.ShowMessageParams
	if decodeErr := json.Unmarshal(req.Params(), &params); decodeErr != nil {
		return nil, false, fmt.Errorf("decode phpactor window/showMessage: %w", decodeErr)
	}
	// phpactor reports indexing progress through window/showMessage because the client declares no work-done progress.
	if strings.HasPrefix(params.Message, phpactorIndexingDoneMessagePrefix) {
		s.finishIndexing(workspaceRoot)
	}

	return nil, false, nil
}

// startIndexing makes reference searches of workspaceRoot wait for the end of indexing of a new phpactor session; a
// state that has not finished stays, so a search that still waits is released by the new session.
func (s *Service) startIndexing(workspaceRoot string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if done, ok := s.indexing[workspaceRoot]; ok && done.isClosed() {
		delete(s.indexing, workspaceRoot)
	}
}

// finishIndexing releases the reference searches of workspaceRoot that wait for the end of indexing.
func (s *Service) finishIndexing(workspaceRoot string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	done := s.indexingDoneLocked(workspaceRoot)
	if !done.isClosed() {
		close(done)
	}
}

// waitForIndexing blocks until the phpactor session of workspaceRoot reports the end of indexing, and returns the
// error of ctx when ctx ends first.
func (s *Service) waitForIndexing(ctx context.Context, workspaceRoot string) error {
	s.mu.Lock()
	done := s.indexingDoneLocked(workspaceRoot)
	s.mu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for phpactor indexing: %w", ctx.Err())
	}
}

// indexingDoneLocked returns the indexing signal of workspaceRoot, creating it on demand, while s.mu is held.
func (s *Service) indexingDoneLocked(workspaceRoot string) indexingDone {
	done, ok := s.indexing[workspaceRoot]
	if !ok {
		done = make(indexingDone)
		s.indexing[workspaceRoot] = done
	}

	return done
}

// Extensions returns the list of file extensions supported by this LSP implementation.
func (*Service) Extensions() []string {
	return extensions
}

// closeSessionAfterCanceledRequest stops phpactor work that can no longer produce a tool response, without making the
// tool response wait for the shutdown.
func (s *Service) closeSessionAfterCanceledRequest(ctx context.Context, workspaceRoot string) {
	if ctx.Err() == nil {
		return
	}

	cleanupCtx := context.WithoutCancel(ctx)
	if err := s.rt.CloseSessionInBackground(cleanupCtx, workspaceRoot); err != nil {
		slog.WarnContext(
			cleanupCtx,
			"close phpactor session after canceled request",
			"err", err,
			"workspace_root", workspaceRoot,
		)
	}
}

// Close shuts down the live phpactor session so process-level cleanup stays explicit.
func (s *Service) Close(ctx context.Context) error {
	return s.rt.Close(ctx)
}
