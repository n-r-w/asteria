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
		URI:  string(uri.File(workspaceRoot)),
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
	capabilities.Workspace.DidChangeWatchedFiles = &protocol.DidChangeWatchedFilesWorkspaceClientCapabilities{
		DynamicRegistration: true,
	}

	return capabilities
}

// runtimeClientCapabilities advertises the baseline standard-LSP features that asteria supports generically.
func runtimeClientCapabilities() protocol.ClientCapabilities {
	return protocol.ClientCapabilities{
		Window: nil,
		Workspace: &protocol.WorkspaceClientCapabilities{
			ApplyEdit:     false,
			WorkspaceEdit: nil,
			DidChangeConfiguration: &protocol.DidChangeConfigurationWorkspaceClientCapabilities{
				DynamicRegistration: true,
			},
			DidChangeWatchedFiles: nil,
			Symbol:                nil,
			ExecuteCommand:        nil,
			WorkspaceFolders:      true,
			Configuration:         true,
			SemanticTokens:        nil,
			CodeLens:              nil,
			FileOperations:        nil,
		},
		TextDocument: &protocol.TextDocumentClientCapabilities{
			Synchronization: &protocol.TextDocumentSyncClientCapabilities{
				DynamicRegistration: true,
				WillSave:            false,
				WillSaveWaitUntil:   false,
				DidSave:             true,
			},
			Completion:    nil,
			Hover:         nil,
			SignatureHelp: nil,
			Declaration:   nil,
			Definition: &protocol.DefinitionTextDocumentClientCapabilities{
				DynamicRegistration: true,
				LinkSupport:         false,
			},
			TypeDefinition:    nil,
			Implementation:    nil,
			References:        nil,
			DocumentHighlight: nil,
			DocumentSymbol: &protocol.DocumentSymbolClientCapabilities{
				DynamicRegistration:               true,
				HierarchicalDocumentSymbolSupport: true,
				SymbolKind:                        &protocol.SymbolKindCapabilities{ValueSet: supportedSymbolKinds()},
				TagSupport:                        nil,
				LabelSupport:                      false,
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
			SemanticTokens:     nil,
			LinkedEditingRange: nil,
			CallHierarchy:      nil,
			Moniker:            nil,
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
	handleServerCallback func(context.Context, jsonrpc2.Replier, jsonrpc2.Request, string) (bool, error),
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
	handleServerCallback   func(context.Context, jsonrpc2.Replier, jsonrpc2.Request, string) (bool, error)
}

// handler acknowledges the subset of callbacks that the runtime can answer generically.
func (c *clientHandler) handler(
	ctx context.Context,
	reply jsonrpc2.Replier,
	req jsonrpc2.Request,
) error {
	if c.handleServerCallback != nil {
		handled, err := c.handleServerCallback(ctx, reply, req, c.workspaceRoot)
		if handled || err != nil {
			return err
		}
	}

	switch req.Method() {
	case protocol.MethodWindowLogMessage,
		protocol.MethodTextDocumentPublishDiagnostics,
		protocol.MethodWindowShowMessage,
		protocol.MethodTelemetryEvent,
		protocol.MethodProgress:
		return reply(ctx, nil, nil)
	case protocol.MethodClientRegisterCapability,
		protocol.MethodClientUnregisterCapability,
		protocol.MethodWorkDoneProgressCreate:
		return reply(ctx, nil, nil)
	case protocol.MethodWorkspaceConfiguration:
		return c.replyConfiguration(ctx, reply, req)
	case protocol.MethodWorkspaceWorkspaceFolders:
		return reply(ctx, c.workspaceFolders, nil)
	default:
		return jsonrpc2.MethodNotFoundHandler(
			ctx,
			reply,
			req,
		)
	}
}

// replyConfiguration returns workspace settings through the injected callback when the runtime has one.
func (c *clientHandler) replyConfiguration(
	ctx context.Context,
	reply jsonrpc2.Replier,
	req jsonrpc2.Request,
) error {
	var params protocol.ConfigurationParams
	if len(req.Params()) > 0 {
		if err := json.Unmarshal(req.Params(), &params); err != nil {
			return reply(ctx, nil, fmt.Errorf("decode workspace/configuration params: %w", err))
		}
	}

	if c.replyConfigurationFunc == nil {
		return reply(ctx, make([]any, len(params.Items)), nil)
	}

	results, err := c.replyConfigurationFunc(c.workspaceRoot, params)
	if err != nil {
		return reply(ctx, nil, err)
	}

	return reply(ctx, results, nil)
}
