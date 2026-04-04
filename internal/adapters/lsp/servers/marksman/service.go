// Package lspmarksman implements the Marksman standard-LSP adapter.
package lspmarksman

import (
	"context"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/server"
	"github.com/n-r-w/asteria/internal/usecase/router"
)

// Service implements Markdown-specific symbolic search logic.
type Service struct {
	rt  *runtimelsp.Runtime
	std *stdlsp.Service
}

var (
	_ router.ILSP = (*Service)(nil)
	_ server.ILSP = (*Service)(nil)
)

// New creates a service that will lazily start Marksman on the first request.
func New() (*Service, error) {
	rt, err := runtimelsp.New(
		&runtimelsp.RuntimeConfig{
			LSPConfig: runtimelsp.LSPConfig{
				Command:                 marksmanServerName,
				Args:                    []string{"server"},
				ServerName:              marksmanServerName,
				ShutdownTimeout:         0,
				ReplyConfiguration:      nil,
				BuildClientCapabilities: nil,
				FileWatch:               nil,
				PatchInitializeParams:   nil,
				HandleServerCallback:    nil,
				AfterInitialized:        nil,
				WaitUntilReady:          nil,
			},
			BuildWorkspaceFolders: nil,
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
		OpenFileForReferenceWorkflow: true,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	})
	if err != nil {
		return nil, err
	}

	return &Service{rt: rt, std: std}, nil
}

// Extensions returns the list of file extensions supported by this LSP implementation.
func (*Service) Extensions() []string {
	return extensions
}

// Close shuts down the live Marksman session so process-level cleanup stays explicit.
func (*Service) Close(_ context.Context) error {
	return nil
}
