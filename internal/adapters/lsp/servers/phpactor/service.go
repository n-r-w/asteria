package lspphpactor

import (
	"context"
	"encoding/json"
	"fmt"
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
	// indexingStartTimeout is the longest time after the start of a session that reference searches wait while no
	// indexing progress has begun; an indexing progress that begins later makes searches wait for its end again.
	indexingStartTimeout time.Duration
	// mu guards indexing.
	mu sync.Mutex
	// indexing holds the indexing state of the current phpactor session of each workspace root.
	indexing map[string]*indexingState
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
		indexingStartTimeout:  phpactorIndexingStartTimeout,
		mu:                    sync.Mutex{},
		indexing:              make(map[string]*indexingState),
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

// buildClientCapabilities advertises the extra hover, reference, and work-done progress features that phpactor needs
// for stable symbol info, member-reference responses, and indexing progress, while reusing the shared runtime baseline
// elsewhere.
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
	// phpactor reports indexing through $/progress only when the client declares work-done progress.
	capabilities.Window = &protocol.WindowClientCapabilities{
		WorkDoneProgress: new(true),
		ShowMessage:      nil,
		ShowDocument:     nil,
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

// handleServerCallback records the announced progress tokens and the indexing progress of the phpactor session of
// workspaceRoot, and leaves each message, except one of these that it cannot decode, to the default handling of the
// runtime.
func (s *Service) handleServerCallback(
	_ context.Context,
	req *jsonrpc2.Request,
	workspaceRoot string,
) (result any, handled bool, err error) {
	switch req.Method() {
	case protocol.MethodWindowWorkDoneProgressCreate:
		var params progressCreateParams
		if decodeErr := json.Unmarshal(req.Params(), &params); decodeErr != nil {
			return nil, false, fmt.Errorf("decode phpactor window/workDoneProgress/create: %w", decodeErr)
		}
		s.recordProgressCreate(workspaceRoot, params.Token)
	case protocol.MethodProgress:
		var params progressParams
		if decodeErr := json.Unmarshal(req.Params(), &params); decodeErr != nil {
			return nil, false, fmt.Errorf("decode phpactor $/progress: %w", decodeErr)
		}
		s.recordProgress(workspaceRoot, params)
	}

	return nil, false, nil
}

// startIndexing makes reference searches of workspaceRoot wait for the indexing of a new phpactor session that starts
// now, and wakes the searches that wait on the previous session so that they follow the new one.
func (s *Service) startIndexing(workspaceRoot string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if previous, ok := s.indexing[workspaceRoot]; ok {
		previous.signalChange()
	}
	s.indexing[workspaceRoot] = newIndexingState()
}

// recordProgressCreate remembers a progress token that the phpactor session of workspaceRoot announced.
func (s *Service) recordProgressCreate(workspaceRoot string, token json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.indexingStateLocked(workspaceRoot).recordCreate(string(token))
}

// recordProgress records the begin and the end of the indexing progress of the phpactor session of workspaceRoot.
func (s *Service) recordProgress(workspaceRoot string, params progressParams) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.indexingStateLocked(workspaceRoot).recordProgress(string(params.Token), params.Value.Kind)
}

// waitForIndexing blocks until the indexing of the current phpactor session of workspaceRoot ends, and returns the
// error of ctx when ctx ends first; it returns false when indexingStartTimeout has passed since the start of the
// current session and no indexing progress has begun in it.
func (s *Service) waitForIndexing(ctx context.Context, workspaceRoot string) (indexingReported bool, err error) {
	for {
		s.mu.Lock()
		state := s.indexingStateLocked(workspaceRoot)
		ended, begun, changed := state.ended, state.hasBegun(), state.changed
		untilStartTimeout := time.Until(state.sessionStart.Add(s.indexingStartTimeout))
		s.mu.Unlock()

		if ended {
			return true, nil
		}
		// A nil channel never fires, so a search waits without the start timeout after the indexing begins.
		var startTimeout <-chan time.Time
		if !begun {
			if untilStartTimeout <= 0 {
				return false, nil
			}
			startTimeout = time.After(untilStartTimeout)
		}
		select {
		case <-changed:
		case <-startTimeout:
		case <-ctx.Done():
			return false, fmt.Errorf("wait for phpactor indexing: %w", ctx.Err())
		}
	}
}

// indexingStateLocked returns the indexing state of workspaceRoot and creates one of a session that starts now when
// absent; the caller must hold s.mu.
func (s *Service) indexingStateLocked(workspaceRoot string) *indexingState {
	state, ok := s.indexing[workspaceRoot]
	if !ok {
		state = newIndexingState()
		s.indexing[workspaceRoot] = state
	}

	return state
}

// Extensions returns the list of file extensions supported by this LSP implementation.
func (*Service) Extensions() []string {
	return extensions
}

// releaseSessionAfterRequest releases the acquired session and, when ctx has ended, shuts it down in the background, so
// that phpactor stops work that no tool response can use.
func releaseSessionAfterRequest(ctx context.Context, acquired *runtimelsp.AcquiredSession) {
	if ctx.Err() == nil {
		acquired.Release()

		return
	}

	acquired.ReleaseAndCloseInBackground(ctx)
}

// Close shuts down the live phpactor session so process-level cleanup stays explicit.
func (s *Service) Close(ctx context.Context) error {
	return s.rt.Close(ctx)
}
