// Package lspgopls implements the gopls standard-LSP adapter.
package lspgopls

import (
	"context"
	"maps"
	"slices"
	"time"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
	"github.com/n-r-w/asteria/internal/server"
	"github.com/n-r-w/asteria/internal/usecase/router"
	"go.lsp.dev/protocol"
)

// Service implements gopls-specific symbolic search logic.
type Service struct {
	rt  *runtimelsp.Runtime
	std *stdlsp.Service
}

const (
	goplsConfigSection    = "gopls"
	goplsSettingsCapacity = 2
	goplsShutdownTimeout  = 15 * time.Second
)

var (
	_ router.ILSP = (*Service)(nil)
	_ server.ILSP = (*Service)(nil)
)

// New creates a service that lazily starts gopls on the first request.
func New(config cfgadapters.GoplsConfig) (*Service, error) {
	rt, err := runtimelsp.New(
		&runtimelsp.RuntimeConfig{
			LSPConfig: runtimelsp.LSPConfig{
				Command:                 "gopls",
				Args:                    nil,
				ServerName:              "gopls",
				ShutdownTimeout:         goplsShutdownTimeout,
				ReplyConfiguration:      buildReplyConfiguration(config),
				BuildClientCapabilities: nil,
				FileWatch: &runtimelsp.FileWatchConfig{
					RelevantFile: shouldWatchGoFile,
					IgnoreDir:    shouldIgnoreDir,
				},
				PatchInitializeParams: nil,
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
		WithRequestDocument:          helpers.WithRequestDocument(func(_ string) string { return goplsLanguageID }),
		OpenFileForDocumentSymbol:    false,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                buildNamePath,
		IgnoreDir:                    shouldIgnoreDir,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	})
	if err != nil {
		return nil, err
	}

	return &Service{
		rt:  rt,
		std: std,
	}, nil
}

// buildReplyConfiguration converts adapter-level gopls settings into one workspace-aware callback.
func buildReplyConfiguration(config cfgadapters.GoplsConfig) func(string, protocol.ConfigurationParams) ([]any, error) {
	if len(config.BuildFlags) == 0 && len(config.Env) == 0 {
		return nil
	}

	settings := make(map[string]any, goplsSettingsCapacity)
	if len(config.BuildFlags) > 0 {
		settings["buildFlags"] = slices.Clone(config.BuildFlags)
	}
	if len(config.Env) > 0 {
		settings["env"] = maps.Clone(config.Env)
	}

	return func(_ string, params protocol.ConfigurationParams) ([]any, error) {
		results := make([]any, len(params.Items))
		for i, item := range params.Items {
			if item.Section == goplsConfigSection {
				results[i] = settings
			}
		}

		return results, nil
	}
}

// Extensions returns the list of file extensions supported by this LSP implementation.
func (s *Service) Extensions() []string {
	return extensions
}

// Close shuts down the live gopls session so process-level cleanup stays explicit.
func (s *Service) Close(ctx context.Context) error {
	return s.rt.Close(ctx)
}
