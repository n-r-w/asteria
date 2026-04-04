package appinit

import (
	lspbasedpyright "github.com/n-r-w/asteria/internal/adapters/lsp/servers/basedpyright"
	lspclangd "github.com/n-r-w/asteria/internal/adapters/lsp/servers/clangd"
	lspgopls "github.com/n-r-w/asteria/internal/adapters/lsp/servers/gopls"
	lspmarksman "github.com/n-r-w/asteria/internal/adapters/lsp/servers/marksman"
	lspphpactor "github.com/n-r-w/asteria/internal/adapters/lsp/servers/phpactor"
	lsprustanalyzer "github.com/n-r-w/asteria/internal/adapters/lsp/servers/rustanalyzer"
	lsptsls "github.com/n-r-w/asteria/internal/adapters/lsp/servers/tsls"
	"github.com/n-r-w/asteria/internal/config"
	"github.com/n-r-w/asteria/internal/usecase/router"
)

const registeredLSPCapacity = 7

// initLSP initializes the LSP implementations and their corresponding close functions.
func initLSP(cfg *config.Config) ([]router.ILSP, []CloseFunc, error) {
	lspImpls := make([]router.ILSP, 0, registeredLSPCapacity)
	closeFuncs := make([]CloseFunc, 0, registeredLSPCapacity)

	// gopls
	goplsImpl, err := lspgopls.New(cfg.Adapters.Gopls)
	if err != nil {
		return nil, nil, err
	}
	lspImpls = append(lspImpls, goplsImpl)
	closeFuncs = append(closeFuncs, goplsImpl.Close)

	// typescript-language-server
	tslsImpl, err := lsptsls.New()
	if err != nil {
		return nil, nil, err
	}
	lspImpls = append(lspImpls, tslsImpl)
	closeFuncs = append(closeFuncs, tslsImpl.Close)

	// marksman
	marksmanImpl, err := lspmarksman.New()
	if err != nil {
		return nil, nil, err
	}
	lspImpls = append(lspImpls, marksmanImpl)
	closeFuncs = append(closeFuncs, marksmanImpl.Close)

	// basedpyright
	basedpyrightImpl, err := lspbasedpyright.New()
	if err != nil {
		return nil, nil, err
	}
	lspImpls = append(lspImpls, basedpyrightImpl)
	closeFuncs = append(closeFuncs, basedpyrightImpl.Close)

	// clangd
	clangdImpl, err := lspclangd.New(cfg.CacheRoot)
	if err != nil {
		return nil, nil, err
	}
	lspImpls = append(lspImpls, clangdImpl)
	closeFuncs = append(closeFuncs, clangdImpl.Close)

	// phpactor
	phpactorImpl, err := lspphpactor.New(cfg.CacheRoot)
	if err != nil {
		return nil, nil, err
	}
	lspImpls = append(lspImpls, phpactorImpl)
	closeFuncs = append(closeFuncs, phpactorImpl.Close)

	// rust-analyzer
	rustAnalyzerImpl, err := lsprustanalyzer.New(cfg.CacheRoot, cfg.Adapters.RustAnalyzer)
	if err != nil {
		return nil, nil, err
	}
	lspImpls = append(lspImpls, rustAnalyzerImpl)
	closeFuncs = append(closeFuncs, rustAnalyzerImpl.Close)

	return lspImpls, closeFuncs, nil
}
