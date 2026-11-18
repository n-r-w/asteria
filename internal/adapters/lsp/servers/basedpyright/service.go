// Package lspbasedpyright implements the basedpyright standard-LSP adapter.
package lspbasedpyright

import (
	"context"
	"time"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"

	"github.com/samber/lo"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/server"
	"github.com/n-r-w/asteria/internal/usecase/router"
)

const experimentalServerStatusMethod = "experimental/serverStatus"

// Service implements basedpyright-specific symbolic search logic.
type Service struct {
	rt                  *runtimelsp.Runtime
	std                 *stdlsp.Service
	withRequestDocument stdlsp.WithRequestDocumentFunc
}

var (
	_ router.ILSP = (*Service)(nil)
	_ server.ILSP = (*Service)(nil)
)

// New creates a service that lazily starts basedpyright on the first request.
func New(sessionIdleTimeout time.Duration) (*Service, error) {
	service := &Service{
		rt:                  nil,
		std:                 nil,
		withRequestDocument: nil,
	}

	rt, err := runtimelsp.New(&runtimelsp.RuntimeConfig{
		Command:                 basedpyrightServerName,
		Args:                    []string{"--stdio"},
		ServerName:              basedpyrightServerName,
		ShutdownTimeout:         0,
		ReplyConfiguration:      buildReplyConfiguration(),
		BuildClientCapabilities: buildClientCapabilities,
		FileWatch:               nil,
		PatchInitializeParams:   service.patchInitializeParams,
		HandleServerCallback:    service.handleServerCallback,
		AfterInitialized:        service.afterInitialized,
		WaitUntilReady:          nil,
		BuildWorkspaceFolders:   nil,
		SessionIdleTimeout:      sessionIdleTimeout,
	})
	if err != nil {
		return nil, err
	}

	withRequestDocument := helpers.WithRequestDocument(func(_ string) string { return pythonLanguageID })

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
	service.std = std
	service.withRequestDocument = withRequestDocument

	return service, nil
}

// buildReplyConfiguration limits background diagnostics to documents opened by symbolic-search workflows.
func buildReplyConfiguration() func(string, protocol.ConfigurationParams) ([]any, error) {
	settings := map[string]any{
		"diagnosticMode": "openFilesOnly",
	}

	return func(_ string, params protocol.ConfigurationParams) ([]any, error) {
		results := make([]any, len(params.Items))
		for i, item := range params.Items {
			if lo.FromPtr(item.Section) == basedpyrightConfigSection {
				results[i] = settings
			}
		}

		return results, nil
	}
}

// buildClientCapabilities adapts the shared runtime baseline with the extra knobs that Pyright-family
// servers expect during startup.
func buildClientCapabilities() protocol.ClientCapabilities {
	capabilities := runtimelsp.DefaultClientCapabilities()
	if capabilities.Workspace == nil {
		capabilities.Workspace = &protocol.WorkspaceClientCapabilities{}
	}
	capabilities.Workspace.Symbol = &protocol.WorkspaceSymbolClientCapabilities{
		DynamicRegistration: new(true),
		SymbolKind:          &protocol.ClientSymbolKindOptions{ValueSet: supportedSymbolKinds()},
		TagSupport:          protocol.ClientSymbolTagOptions{},
		ResolveSupport:      protocol.ClientSymbolResolveOptions{},
	}
	capabilities.Workspace.ExecuteCommand = &protocol.ExecuteCommandClientCapabilities{
		DynamicRegistration: new(true),
	}

	if capabilities.TextDocument == nil {
		capabilities.TextDocument = &protocol.TextDocumentClientCapabilities{}
	}
	if capabilities.TextDocument.Synchronization == nil {
		capabilities.TextDocument.Synchronization = &protocol.TextDocumentSyncClientCapabilities{}
	}
	capabilities.TextDocument.Synchronization.DynamicRegistration = new(true)
	capabilities.TextDocument.Synchronization.WillSave = new(true)
	capabilities.TextDocument.Synchronization.WillSaveWaitUntil = new(true)
	capabilities.TextDocument.Synchronization.DidSave = new(true)
	capabilities.TextDocument.Hover = &protocol.HoverClientCapabilities{
		DynamicRegistration: new(true),
		ContentFormat:       []protocol.MarkupKind{protocol.MarkupKindMarkdown, protocol.MarkupKindPlainText},
	}
	capabilities.TextDocument.SignatureHelp = &protocol.SignatureHelpClientCapabilities{
		DynamicRegistration: new(true),
		SignatureInformation: &protocol.ClientSignatureInformationOptions{
			DocumentationFormat: []protocol.MarkupKind{protocol.MarkupKindMarkdown, protocol.MarkupKindPlainText},
			ParameterInformation: &protocol.ClientSignatureParameterInformationOptions{
				LabelOffsetSupport: new(true),
			},
			ActiveParameterSupport:   nil,
			NoActiveParameterSupport: nil,
		},
		ContextSupport: nil,
	}
	capabilities.TextDocument.References = &protocol.ReferenceClientCapabilities{
		DynamicRegistration: new(true),
	}
	capabilities.TextDocument.PublishDiagnostics = &protocol.PublishDiagnosticsClientCapabilities{
		RelatedInformation:     new(true),
		TagSupport:             protocol.ClientDiagnosticsTagOptions{},
		CodeDescriptionSupport: nil,
		DataSupport:            nil,
		VersionSupport:         nil,
	}

	return capabilities
}

// patchInitializeParams adds the Pyright-specific startup parameters that the stable Serena implementation
// relies on, while still reusing the shared runtime handshake.
func (s *Service) patchInitializeParams(_ string, params *protocol.InitializeParams) error {
	initializationOptions, err := helpers.MarshalLSPAny(map[string]any{
		"exclude": []string{
			"**/__pycache__",
			"**/.venv",
			"**/.env",
			"**/build",
			"**/dist",
			"**/.pixi",
		},
		"reportMissingImports": "error",
	})
	if err != nil {
		return err
	}
	params.InitializationOptions = initializationOptions

	return nil
}

// handleServerCallback acknowledges the extra callbacks that basedpyright emits outside the generic runtime set.
func (s *Service) handleServerCallback(
	_ context.Context,
	req *jsonrpc2.Request,
	_ string,
) (result any, handled bool, err error) {
	switch req.Method() {
	case "workspace/executeClientCommand":
		return []any{}, true, nil
	case "language/status",
		"language/actionableNotification",
		experimentalServerStatusMethod:
		return nil, true, nil
	default:
		return nil, false, nil
	}
}

// afterInitialized triggers the configuration refresh that basedpyright needs before it answers documentSymbol.
func (s *Service) afterInitialized(ctx context.Context, conn jsonrpc2.Conn, _ string) error {
	return conn.Notify(ctx, protocol.MethodWorkspaceDidChangeConfiguration, &protocol.DidChangeConfigurationParams{
		Settings: protocol.LSPAny("{}"),
	})
}

// supportedSymbolKinds keeps the workspace-symbol capability aligned with the full LSP enum range.
func supportedSymbolKinds() []protocol.SymbolKind {
	supportedKinds := make([]protocol.SymbolKind, 0, int(protocol.SymbolKindTypeParameter))
	for rawKind := int(protocol.SymbolKindFile); rawKind <= int(protocol.SymbolKindTypeParameter); rawKind++ {
		supportedKinds = append(supportedKinds, protocol.SymbolKind(rawKind))
	}

	return supportedKinds
}

// Extensions returns the list of file extensions supported by this LSP implementation.
func (*Service) Extensions() []string {
	return extensions
}

// Close shuts down the live basedpyright session so process-level cleanup stays explicit.
func (s *Service) Close(ctx context.Context) error {
	return s.rt.Close(ctx)
}
