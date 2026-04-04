package runtimelsp

import (
	"context"
	"time"

	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// LSPConfig describes the shared runtime/session settings for one concrete language server.
type LSPConfig struct {
	Command    string
	Args       []string
	ServerName string

	ShutdownTimeout         time.Duration
	ReplyConfiguration      func(workspaceRoot string, params protocol.ConfigurationParams) ([]any, error)
	BuildClientCapabilities func() protocol.ClientCapabilities
	FileWatch               *FileWatchConfig
	PatchInitializeParams   func(workspaceRoot string, params *protocol.InitializeParams) error
	HandleServerCallback    func(context.Context, jsonrpc2.Replier, jsonrpc2.Request, string) (bool, error)
	AfterInitialized        func(context.Context, jsonrpc2.Conn, string) error
	WaitUntilReady          func(context.Context, jsonrpc2.Conn, string) error
}

// FileWatchConfig describes runtime-managed workspace file watching for one LSP session.
type FileWatchConfig struct {
	RelevantFile func(relativePath string) bool
	IgnoreDir    func(relativePath string) bool
}

// RuntimeConfig describes how to start and initialize one concrete language server process.
type RuntimeConfig struct {
	LSPConfig

	BuildWorkspaceFolders func(workspaceRoot string) []protocol.WorkspaceFolder
}

// SessionInfo describes read-only metadata captured from one initialized LSP session.
type SessionInfo struct {
	// InitializeResult keeps the server's initialize response so adapters can build stable cache fingerprints.
	InitializeResult *protocol.InitializeResult
}

// sessionConfig describes one concrete root-bound language-server session.
type sessionConfig struct {
	LSPConfig

	WorkspaceRoot    string
	WorkspaceFolders []protocol.WorkspaceFolder
}
