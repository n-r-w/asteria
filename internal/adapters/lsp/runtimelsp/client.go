package runtimelsp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// currentProcessID keeps the LSP handshake explicit and avoids lossy integer casts.
func currentProcessID() (int32, error) {
	processID := os.Getpid()
	if processID < 0 || processID > math.MaxInt32 {
		return 0, fmt.Errorf("process id %d is out of int32 range", processID)
	}

	return int32(processID), nil
}

// defaultWorkspaceFolders derives the single-root workspace view used by the default client handler.
func defaultWorkspaceFolders(workspaceRoot string) []protocol.WorkspaceFolder {
	return []protocol.WorkspaceFolder{{
		URI:  uri.File(workspaceRoot),
		Name: filepath.Base(workspaceRoot),
	}}
}

// DefaultClientCapabilities returns the shared baseline client capabilities used by standard-LSP adapters.
func DefaultClientCapabilities() protocol.ClientCapabilities {
	return runtimeClientCapabilities()
}

// enableDidChangeWatchedFilesCapability augments one client capability set with standard watched-files support.
func enableDidChangeWatchedFilesCapability(
	capabilities protocol.ClientCapabilities,
) protocol.ClientCapabilities {
	if capabilities.Workspace == nil {
		capabilities.Workspace = &protocol.WorkspaceClientCapabilities{}
	}
	capabilities.Workspace.DidChangeWatchedFiles = &protocol.DidChangeWatchedFilesClientCapabilities{
		DynamicRegistration:    new(true),
		RelativePatternSupport: nil,
	}

	return capabilities
}

// runtimeClientCapabilities advertises the baseline standard-LSP features that asteria supports generically.
func runtimeClientCapabilities() protocol.ClientCapabilities {
	return protocol.ClientCapabilities{
		Window:           nil,
		NotebookDocument: nil,
		Workspace: &protocol.WorkspaceClientCapabilities{
			ApplyEdit:     nil,
			WorkspaceEdit: nil,
			DidChangeConfiguration: &protocol.DidChangeConfigurationClientCapabilities{
				DynamicRegistration: new(true),
			},
			DidChangeWatchedFiles: nil,
			Symbol:                nil,
			ExecuteCommand:        nil,
			WorkspaceFolders:      new(true),
			Configuration:         new(true),
			SemanticTokens:        nil,
			CodeLens:              nil,
			FileOperations:        nil,
			InlineValue:           nil,
			InlayHint:             nil,
			Diagnostics:           nil,
			FoldingRange:          nil,
			TextDocumentContent:   nil,
		},
		TextDocument: &protocol.TextDocumentClientCapabilities{
			Synchronization: &protocol.TextDocumentSyncClientCapabilities{
				DynamicRegistration: new(true),
				WillSave:            nil,
				WillSaveWaitUntil:   nil,
				DidSave:             new(true),
			},
			Completion:    nil,
			Hover:         nil,
			SignatureHelp: nil,
			Declaration:   nil,
			Definition: &protocol.DefinitionClientCapabilities{
				DynamicRegistration: new(true),
				LinkSupport:         nil,
			},
			TypeDefinition:    nil,
			Implementation:    nil,
			References:        nil,
			DocumentHighlight: nil,
			DocumentSymbol: &protocol.DocumentSymbolClientCapabilities{
				DynamicRegistration:               new(true),
				HierarchicalDocumentSymbolSupport: new(true),
				SymbolKind:                        &protocol.ClientSymbolKindOptions{ValueSet: supportedSymbolKinds()},
				TagSupport:                        protocol.ClientSymbolTagOptions{},
				LabelSupport:                      nil,
			},
			CodeAction:         nil,
			CodeLens:           nil,
			DocumentLink:       nil,
			ColorProvider:      nil,
			Formatting:         nil,
			RangeFormatting:    nil,
			OnTypeFormatting:   nil,
			PublishDiagnostics: nil,
			Rename:             nil,
			FoldingRange:       nil,
			SelectionRange:     nil,
			SemanticTokens:     protocol.SemanticTokensClientCapabilities{},
			LinkedEditingRange: nil,
			CallHierarchy:      nil,
			Moniker:            nil,
			Filters:            nil,
			TypeHierarchy:      nil,
			InlineValue:        nil,
			InlayHint:          nil,
			Diagnostic:         nil,
			InlineCompletion:   nil,
		},
		General:      nil,
		Experimental: nil,
	}
}

// supportedSymbolKinds keeps the advertised symbol-kind range aligned with the full LSP enum set.
func supportedSymbolKinds() []protocol.SymbolKind {
	supportedKinds := make([]protocol.SymbolKind, 0, int(protocol.SymbolKindTypeParameter))
	for rawKind := int(protocol.SymbolKindFile); rawKind <= int(protocol.SymbolKindTypeParameter); rawKind++ {
		supportedKinds = append(supportedKinds, protocol.SymbolKind(rawKind))
	}

	return supportedKinds
}

// newClientHandler builds the baseline callback handler used during server startup.
func newClientHandler(
	workspaceRoot string,
	workspaceFolders []protocol.WorkspaceFolder,
	replyConfiguration func(string, protocol.ConfigurationParams) ([]any, error),
	handleServerCallback ServerCallbackHandler,
) jsonrpc2.Handler {
	handler := &clientHandler{
		workspaceRoot:          workspaceRoot,
		workspaceFolders:       workspaceFolders,
		replyConfigurationFunc: replyConfiguration,
		handleServerCallback:   handleServerCallback,
	}

	return handler.handler
}

// clientHandler stores the client state needed to answer baseline workspace callbacks.
type clientHandler struct {
	workspaceRoot          string
	workspaceFolders       []protocol.WorkspaceFolder
	replyConfigurationFunc func(string, protocol.ConfigurationParams) ([]any, error)
	handleServerCallback   ServerCallbackHandler
}

// handler acknowledges the subset of callbacks that the runtime can answer generically.
func (c *clientHandler) handler(ctx context.Context, req *jsonrpc2.Request) (any, error) {
	if c.handleServerCallback != nil {
		result, handled, err := c.handleServerCallback(ctx, req, c.workspaceRoot)
		if handled || err != nil {
			return result, err
		}
	}

	switch req.Method() {
	case protocol.MethodWindowLogMessage,
		protocol.MethodTextDocumentPublishDiagnostics,
		protocol.MethodWindowShowMessage,
		protocol.MethodTelemetryEvent,
		protocol.MethodProgress,
		protocol.MethodClientRegisterCapability,
		protocol.MethodClientUnregisterCapability,
		protocol.MethodWindowWorkDoneProgressCreate:
		//nolint:nilnil // A nil result is the JSON-RPC null reply, and a notification gets no reply.
		return nil, nil
	case protocol.MethodWorkspaceConfiguration:
		return c.replyConfiguration(req)
	case protocol.MethodWorkspaceWorkspaceFolders:
		return c.workspaceFolders, nil
	default:
		return jsonrpc2.MethodNotFoundHandler(ctx, req)
	}
}

// replyConfiguration returns workspace settings through the injected callback when the runtime has one.
func (c *clientHandler) replyConfiguration(req *jsonrpc2.Request) (any, error) {
	var params protocol.ConfigurationParams
	if len(req.Params()) > 0 {
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return nil, fmt.Errorf("decode workspace/configuration params: %w", err)
		}
	}

	if c.replyConfigurationFunc == nil {
		return make([]any, len(params.Items)), nil
	}

	return c.replyConfigurationFunc(c.workspaceRoot, params)
}
