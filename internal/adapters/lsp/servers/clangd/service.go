package lspclangd

import (
	"context"

	lspcache "github.com/n-r-w/asteria/internal/adapters/lsp/cache"
	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/server"
	"github.com/n-r-w/asteria/internal/usecase/router"
)

// Service implements clangd-specific symbolic search logic.
type Service struct {
	rt                  *runtimelsp.Runtime
	std                 *stdlsp.Service
	withRequestDocument stdlsp.WithRequestDocumentFunc
	cacheRoot           string
}

var (
	_ router.ILSP = (*Service)(nil)
	_ server.ILSP = (*Service)(nil)
)

// New creates a service that lazily starts clangd on the first request.
func New(cacheRoot string) (*Service, error) {
	service := &Service{
		rt:                  nil,
		std:                 nil,
		withRequestDocument: nil,
		cacheRoot:           "",
	}

	normalizedCacheRoot, err := helpers.ResolveCacheRoot(cacheRoot)
	if err != nil {
		return nil, err
	}
	service.cacheRoot = normalizedCacheRoot
	symbolTreeCache, err := lspcache.New(normalizedCacheRoot)
	if err != nil {
		return nil, err
	}

	withRequestDocument := helpers.WithRequestDocument(languageIDForExtension)

	rt, err := runtimelsp.New(&runtimelsp.RuntimeConfig{
		LSPConfig: runtimelsp.LSPConfig{
			Command:                 clangdServerName,
			Args:                    []string{"--background-index"},
			ServerName:              clangdServerName,
			ShutdownTimeout:         0,
			ReplyConfiguration:      nil,
			BuildClientCapabilities: nil,
			FileWatch: &runtimelsp.FileWatchConfig{
				RelevantFile: shouldWatchClangdFile,
				IgnoreDir:    shouldIgnoreDir,
			},
			PatchInitializeParams: service.patchInitializeParams,
			HandleServerCallback:  nil,
			AfterInitialized:      nil,
			WaitUntilReady:        nil,
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
		SymbolTreeCache:              symbolTreeCache,
		BuildSymbolTreeCacheMetadata: service.buildSymbolTreeCacheMetadata,
	})
	if err != nil {
		return nil, err
	}

	service.rt = rt
	service.std = std
	service.withRequestDocument = withRequestDocument

	return service, nil
}

// Extensions returns the list of file extensions supported by this LSP implementation.
func (*Service) Extensions() []string {
	return extensions
}

// Close shuts down the live clangd session so process-level cleanup stays explicit.
func (s *Service) Close(ctx context.Context) error {
	return s.rt.Close(ctx)
}
