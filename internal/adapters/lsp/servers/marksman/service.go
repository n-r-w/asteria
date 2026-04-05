// Package lspmarksman implements the Marksman standard-LSP adapter.
package lspmarksman

import (
	"context"
	"path/filepath"
	"strings"

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
				FileWatch: &runtimelsp.FileWatchConfig{
					RelevantFile: shouldWatchMarksmanFile,
					IgnoreDir:    nil,
				},
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

// shouldWatchMarksmanFile keeps runtime-managed watched-files focused on supported Markdown sources.
func shouldWatchMarksmanFile(relativePath string) bool {
	extension := filepath.Ext(relativePath)
	for _, supportedExtension := range extensions {
		if strings.EqualFold(extension, supportedExtension) {
			return true
		}
	}

	return false
}

// Close shuts down the live Marksman session so process-level cleanup stays explicit.
func (s *Service) Close(ctx context.Context) error {
	return s.rt.Close(ctx)
}
