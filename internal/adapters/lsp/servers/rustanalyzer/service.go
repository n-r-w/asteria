package lsprustanalyzer

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
	"github.com/n-r-w/asteria/internal/server"
	"github.com/n-r-w/asteria/internal/usecase/router"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// startupReadinessState tracks one in-flight rust-analyzer startup until the server reports that
// workspace analysis has reached the quiescent state needed for stable references.
type startupReadinessState struct {
	readyCh chan struct{}
	ready   bool
}

// Service implements rust-analyzer-specific symbolic search logic.
type Service struct {
	rt                  *runtimelsp.Runtime
	std                 *stdlsp.Service
	stdReferences       *stdlsp.Service
	withRequestDocument stdlsp.WithRequestDocumentFunc
	cacheRoot           string
	mu                  sync.Mutex
	startupReadiness    map[string]*startupReadinessState

	workspaceSymbolSearchLimit int
	startupReadyTimeout        time.Duration
}

var (
	_ router.ILSP = (*Service)(nil)
	_ server.ILSP = (*Service)(nil)
)

// New creates a service that lazily starts rust-analyzer on the first request while isolating Cargo build
// artifacts inside the managed cache root.
func New(cacheRoot string, cfg cfgadapters.RustAnalyzerConfig) (*Service, error) {
	normalizedCacheRoot, err := helpers.ResolveCacheRoot(cacheRoot)
	if err != nil {
		return nil, err
	}

	service := &Service{
		rt:                         nil,
		std:                        nil,
		stdReferences:              nil,
		withRequestDocument:        nil,
		cacheRoot:                  normalizedCacheRoot,
		mu:                         sync.Mutex{},
		startupReadiness:           make(map[string]*startupReadinessState),
		workspaceSymbolSearchLimit: cfg.WorkspaceSymbolSearchLimit,
		startupReadyTimeout:        cfg.StartupReadyTimeout,
	}

	withRequestDocument := helpers.WithRequestDocument(func(_ string) string { return rustLanguageID })

	rt, err := runtimelsp.New(&runtimelsp.RuntimeConfig{
		LSPConfig: runtimelsp.LSPConfig{
			Command:                 rustAnalyzerServerName,
			Args:                    nil,
			ServerName:              rustAnalyzerServerName,
			ShutdownTimeout:         0,
			ReplyConfiguration:      nil,
			BuildClientCapabilities: buildClientCapabilities,
			FileWatch:               nil,
			PatchInitializeParams:   service.patchInitializeParams,
			HandleServerCallback:    service.handleServerCallback,
			AfterInitialized:        nil,
			WaitUntilReady:          service.waitUntilReady,
		},
		BuildWorkspaceFolders: nil,
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

	stdReferences, err := stdlsp.New(&stdlsp.Config{
		Extensions:                   extensions,
		EnsureConn:                   rt.EnsureConn,
		WithRequestDocument:          nil,
		OpenFileForDocumentSymbol:    false,
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
	service.std = std
	service.stdReferences = stdReferences
	service.withRequestDocument = withRequestDocument

	return service, nil
}

// buildClientCapabilities requests the Rust-specific capability subset that exposes workspace indexing,
// reference lookup, and startup readiness signals without copying Serena's editor-only payload.
func buildClientCapabilities() protocol.ClientCapabilities {
	capabilities := runtimelsp.DefaultClientCapabilities()
	if capabilities.Workspace == nil {
		capabilities.Workspace = &protocol.WorkspaceClientCapabilities{}
	}
	capabilities.Workspace.DidChangeWatchedFiles = &protocol.DidChangeWatchedFilesWorkspaceClientCapabilities{
		DynamicRegistration: true,
	}
	capabilities.Workspace.ExecuteCommand = &protocol.ExecuteCommandClientCapabilities{
		DynamicRegistration: true,
	}

	var symbolKinds *protocol.SymbolKindCapabilities
	if capabilities.TextDocument != nil && capabilities.TextDocument.DocumentSymbol != nil {
		symbolKinds = capabilities.TextDocument.DocumentSymbol.SymbolKind
	}
	capabilities.Workspace.Symbol = &protocol.WorkspaceSymbolClientCapabilities{
		DynamicRegistration: true,
		SymbolKind:          symbolKinds,
		TagSupport:          nil,
	}

	if capabilities.TextDocument == nil {
		capabilities.TextDocument = &protocol.TextDocumentClientCapabilities{}
	}
	if capabilities.TextDocument.Synchronization == nil {
		capabilities.TextDocument.Synchronization = &protocol.TextDocumentSyncClientCapabilities{}
	}
	capabilities.TextDocument.Synchronization.DynamicRegistration = true
	capabilities.TextDocument.Synchronization.WillSave = true
	capabilities.TextDocument.Synchronization.WillSaveWaitUntil = true
	capabilities.TextDocument.Synchronization.DidSave = true
	capabilities.TextDocument.References = &protocol.ReferencesTextDocumentClientCapabilities{
		DynamicRegistration: true,
	}
	capabilities.TextDocument.Hover = &protocol.HoverTextDocumentClientCapabilities{
		DynamicRegistration: true,
		ContentFormat:       []protocol.MarkupKind{protocol.Markdown, protocol.PlainText},
	}
	capabilities.TextDocument.PublishDiagnostics = &protocol.PublishDiagnosticsClientCapabilities{
		RelatedInformation:     true,
		TagSupport:             nil,
		VersionSupport:         false,
		CodeDescriptionSupport: false,
		DataSupport:            false,
	}

	capabilities.Window = &protocol.WindowClientCapabilities{
		WorkDoneProgress: true,
		ShowMessage:      nil,
		ShowDocument:     nil,
	}
	capabilities.Experimental = map[string]any{
		"serverStatusNotification": true,
	}

	return capabilities
}

// patchInitializeParams adds the Rust-specific initialization options that Serena uses for workspace loading,
// indexing, symbols, and references, while still preserving Asteria's workspaceFolders-only root contract.
func (s *Service) patchInitializeParams(workspaceRoot string, params *protocol.InitializeParams) error {
	cargoTargetDir, err := s.cargoTargetDir(workspaceRoot)
	if err != nil {
		return err
	}

	params.InitializationOptions = map[string]any{
		"cargo": map[string]any{
			"autoreload": true,
			"extraEnv": map[string]any{
				"CARGO_TARGET_DIR": cargoTargetDir,
			},
			"buildScripts": map[string]any{
				"enable":             true,
				"invocationLocation": "workspace",
				"invocationStrategy": "per_workspace",
			},
		},
		"procMacro": map[string]any{
			"enable": true,
			"attributes": map[string]any{
				"enable": true,
			},
		},
		"checkOnSave":    false,
		"linkedProjects": []any{},
		"workspace": map[string]any{
			"symbol": map[string]any{
				"search": map[string]any{
					"kind":  "only_types",
					"limit": s.workspaceSymbolSearchLimit,
					"scope": "workspace",
				},
			},
		},
		"diagnostics": map[string]any{
			"enable": true,
		},
	}

	return nil
}

// cargoTargetDir returns the managed Cargo target directory for one Rust workspace so rust-analyzer-triggered
// cargo invocations keep their artifacts out of the analyzed repository tree.
func (s *Service) cargoTargetDir(workspaceRoot string) (string, error) {
	adapterCacheDir, err := helpers.AdapterCacheDir(s.cacheRoot, workspaceRoot, rustAnalyzerServerName)
	if err != nil {
		return "", err
	}

	return filepath.Join(adapterCacheDir, rustAnalyzerCargoTargetDirName), nil
}

// handleServerCallback acknowledges the extra rust-analyzer callback that appears during workspace analysis
// but is not yet handled by the shared runtime callback set.
func (s *Service) handleServerCallback(
	ctx context.Context,
	reply jsonrpc2.Replier,
	req jsonrpc2.Request,
	workspaceRoot string,
) (bool, error) {
	switch req.Method() {
	case experimentalServerStatusMethod:
		params := &struct {
			Quiescent bool `json:"quiescent"`
		}{}
		if len(req.Params()) > 0 {
			if err := json.Unmarshal(req.Params(), params); err != nil {
				return true, reply(ctx, nil, fmt.Errorf("decode rust-analyzer server status: %w", err))
			}
		}
		if params.Quiescent {
			s.markStartupReady(workspaceRoot)
		}

		return true, reply(ctx, nil, nil)
	case workspaceDiagnosticRefreshMethod:
		return true, reply(ctx, nil, nil)
	default:
		return false, nil
	}
}

// waitUntilReady blocks session startup until rust-analyzer reports its quiescent workspace-analysis state,
// which live probes showed is the point where the first cross-file reference request stops returning transient errors.
func (s *Service) waitUntilReady(ctx context.Context, _ jsonrpc2.Conn, workspaceRoot string) error {
	state := s.startupState(workspaceRoot)
	defer s.clearStartupState(workspaceRoot, state)

	waitCtx, cancel := context.WithTimeout(ctx, s.startupReadyTimeout)
	defer cancel()

	select {
	case <-state.readyCh:
		return nil
	case <-waitCtx.Done():
		return fmt.Errorf("wait for rust-analyzer quiescent startup: %w", waitCtx.Err())
	}
}

// startupState returns the current start-attempt state for one workspace root, creating it on demand so
// callback delivery and startup waiting can meet on the same channel.
func (s *Service) startupState(workspaceRoot string) *startupReadinessState {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.startupStateLocked(workspaceRoot)
}

// startupStateLocked centralizes start-attempt state creation while the service mutex is held.
func (s *Service) startupStateLocked(workspaceRoot string) *startupReadinessState {
	state := s.startupReadiness[workspaceRoot]
	if state != nil {
		return state
	}

	state = &startupReadinessState{readyCh: make(chan struct{}), ready: false}
	s.startupReadiness[workspaceRoot] = state

	return state
}

// markStartupReady releases the current start-attempt waiter once rust-analyzer reports quiescent workspace state.
func (s *Service) markStartupReady(workspaceRoot string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.startupStateLocked(workspaceRoot)
	if state.ready {
		return
	}

	state.ready = true
	close(state.readyCh)
}

// clearStartupState removes one completed start-attempt state so restart attempts cannot reuse stale readiness.
func (s *Service) clearStartupState(workspaceRoot string, state *startupReadinessState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.startupReadiness[workspaceRoot] == state {
		delete(s.startupReadiness, workspaceRoot)
	}
}

// Extensions returns the list of file extensions supported by this LSP implementation.
func (*Service) Extensions() []string {
	return extensions
}

// Close shuts down the live rust-analyzer session so process-level cleanup stays explicit.
func (s *Service) Close(ctx context.Context) error {
	return s.rt.Close(ctx)
}
