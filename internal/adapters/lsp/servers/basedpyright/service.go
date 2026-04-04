// Package lspbasedpyright implements the basedpyright standard-LSP adapter.
package lspbasedpyright

import (
	"context"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/server"
	"github.com/n-r-w/asteria/internal/usecase/router"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
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
func New() (*Service, error) {
	service := &Service{
		rt:                  nil,
		std:                 nil,
		withRequestDocument: nil,
	}

	rt, err := runtimelsp.New(&runtimelsp.RuntimeConfig{
		LSPConfig: runtimelsp.LSPConfig{
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
		},
		BuildWorkspaceFolders: nil,
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

// buildReplyConfiguration keeps basedpyright in workspace-analysis mode so cross-file symbol and reference
// queries use the whole selected workspace rather than only transiently opened files.
func buildReplyConfiguration() func(string, protocol.ConfigurationParams) ([]any, error) {
	settings := map[string]any{
		"diagnosticMode": "workspace",
	}

	return func(_ string, params protocol.ConfigurationParams) ([]any, error) {
		results := make([]any, len(params.Items))
		for i, item := range params.Items {
			if item.Section == basedpyrightConfigSection {
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
		DynamicRegistration: true,
		SymbolKind:          &protocol.SymbolKindCapabilities{ValueSet: supportedSymbolKinds()},
		TagSupport:          nil,
	}
	capabilities.Workspace.ExecuteCommand = &protocol.ExecuteCommandClientCapabilities{
		DynamicRegistration: true,
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
	capabilities.TextDocument.Hover = &protocol.HoverTextDocumentClientCapabilities{
		DynamicRegistration: true,
		ContentFormat:       []protocol.MarkupKind{protocol.Markdown, protocol.PlainText},
	}
	capabilities.TextDocument.SignatureHelp = &protocol.SignatureHelpTextDocumentClientCapabilities{
		DynamicRegistration: true,
		SignatureInformation: &protocol.TextDocumentClientCapabilitiesSignatureInformation{
			DocumentationFormat: []protocol.MarkupKind{protocol.Markdown, protocol.PlainText},
			ParameterInformation: &protocol.TextDocumentClientCapabilitiesParameterInformation{
				LabelOffsetSupport: true,
			},
			ActiveParameterSupport: false,
		},
		ContextSupport: false,
	}
	capabilities.TextDocument.References = &protocol.ReferencesTextDocumentClientCapabilities{
		DynamicRegistration: true,
	}
	capabilities.TextDocument.PublishDiagnostics = &protocol.PublishDiagnosticsClientCapabilities{
		RelatedInformation:     true,
		TagSupport:             nil,
		VersionSupport:         false,
		CodeDescriptionSupport: false,
		DataSupport:            false,
	}

	return capabilities
}

// patchInitializeParams adds the Pyright-specific startup parameters that the stable Serena implementation
// relies on, while still reusing the shared runtime handshake.
func (s *Service) patchInitializeParams(_ string, params *protocol.InitializeParams) error {
	params.InitializationOptions = map[string]any{
		"exclude": []string{
			"**/__pycache__",
			"**/.venv",
			"**/.env",
			"**/build",
			"**/dist",
			"**/.pixi",
		},
		"reportMissingImports": "error",
	}

	return nil
}

// handleServerCallback acknowledges the extra callbacks that basedpyright emits outside the generic runtime set.
func (s *Service) handleServerCallback(
	ctx context.Context,
	reply jsonrpc2.Replier,
	req jsonrpc2.Request,
	_ string,
) (bool, error) {
	switch req.Method() {
	case "workspace/executeClientCommand":
		return true, reply(ctx, []any{}, nil)
	case "language/status",
		"language/actionableNotification",
		experimentalServerStatusMethod:
		return true, reply(ctx, nil, nil)
	default:
		return false, nil
	}
}

// afterInitialized triggers the configuration refresh that basedpyright needs before it answers documentSymbol.
func (s *Service) afterInitialized(ctx context.Context, conn jsonrpc2.Conn, _ string) error {
	return conn.Notify(ctx, protocol.MethodWorkspaceDidChangeConfiguration, &protocol.DidChangeConfigurationParams{
		Settings: map[string]any{},
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
