package lspphpactor

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/server"
	"github.com/n-r-w/asteria/internal/usecase/router"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// Service implements phpactor-specific symbolic search logic.
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

// New creates a service that lazily starts phpactor on the first request while keeping adapter state inside
// the managed cache root instead of the analyzed workspace.
func New(cacheRoot string) (*Service, error) {
	normalizedCacheRoot, err := helpers.ResolveCacheRoot(cacheRoot)
	if err != nil {
		return nil, err
	}

	withRequestDocument := helpers.WithRequestDocument(func(_ string) string { return phpLanguageID })
	service := &Service{
		rt:                  nil,
		std:                 nil,
		withRequestDocument: nil,
		cacheRoot:           normalizedCacheRoot,
	}

	rt, err := runtimelsp.New(&runtimelsp.RuntimeConfig{
		LSPConfig: runtimelsp.LSPConfig{
			Command:                 phpactorServerName,
			Args:                    []string{"language-server"},
			ServerName:              phpactorServerName,
			ShutdownTimeout:         0,
			ReplyConfiguration:      nil,
			BuildClientCapabilities: buildClientCapabilities,
			FileWatch: &runtimelsp.FileWatchConfig{
				RelevantFile: shouldWatchPHPFile,
				IgnoreDir:    shouldIgnoreDir,
			},
			PatchInitializeParams: service.patchInitializeParams,
			HandleServerCallback:  nil,
			AfterInitialized:      service.ensureIndexerPathReady,
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

// buildClientCapabilities advertises the extra hover and reference features that phpactor needs for stable
// symbol info and member-reference responses, while reusing the shared runtime baseline elsewhere.
func buildClientCapabilities() protocol.ClientCapabilities {
	capabilities := runtimelsp.DefaultClientCapabilities()
	if capabilities.TextDocument == nil {
		capabilities.TextDocument = &protocol.TextDocumentClientCapabilities{}
	}
	capabilities.TextDocument.Hover = &protocol.HoverTextDocumentClientCapabilities{
		DynamicRegistration: true,
		ContentFormat:       []protocol.MarkupKind{protocol.Markdown, protocol.PlainText},
	}
	capabilities.TextDocument.References = &protocol.ReferencesTextDocumentClientCapabilities{
		DynamicRegistration: true,
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

// Extensions returns the list of file extensions supported by this LSP implementation.
func (*Service) Extensions() []string {
	return extensions
}

// Close shuts down the live phpactor session so process-level cleanup stays explicit.
func (s *Service) Close(ctx context.Context) error {
	return s.rt.Close(ctx)
}
