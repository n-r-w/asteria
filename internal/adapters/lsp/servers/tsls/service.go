// Package lsptsls implements the TypeScript standard-LSP adapter.
package lsptsls

import (
	"context"

	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/server"
	"github.com/n-r-w/asteria/internal/usecase/router"
)

// Service implements TypeScript-specific symbolic search logic.
type Service struct {
	*stdlsp.Service
	rt                  *runtimelsp.Runtime
	withRequestDocument stdlsp.WithRequestDocumentFunc
}

var (
	_ router.ILSP = (*Service)(nil)
	_ server.ILSP = (*Service)(nil)
)

// New creates a service that lazily starts the TypeScript language server on the first request.
func New() (*Service, error) {
	rt, err := runtimelsp.New(
		&runtimelsp.RuntimeConfig{
			Command:                 tslsServerName,
			Args:                    []string{"--stdio"},
			ServerName:              tslsServerName,
			ShutdownTimeout:         0,
			ReplyConfiguration:      nil,
			BuildClientCapabilities: nil,
			FileWatch:               nil,
			PatchInitializeParams:   patchInitializeParams,
			HandleServerCallback:    nil,
			AfterInitialized:        nil,
			WaitUntilReady:          nil,
			BuildWorkspaceFolders:   nil,
		})
	if err != nil {
		return nil, err
	}
	withRequestDocument := helpers.WithRequestDocument(languageIDForExtension)

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

	return &Service{Service: std, rt: rt, withRequestDocument: withRequestDocument}, nil
}

func patchInitializeParams(workspaceRoot string, params *protocol.InitializeParams) error {
	//nolint:staticcheck // Supported typescript-language-server versions require RootURI to find workspace TypeScript.
	params.RootURI = uri.File(workspaceRoot)

	return nil
}

// Extensions returns the list of file extensions supported by this LSP implementation.
func (*Service) Extensions() []string {
	return extensions
}

// Close shuts down the live TypeScript language server session so process-level cleanup stays explicit.
func (s *Service) Close(ctx context.Context) error {
	return s.rt.Close(ctx)
}
