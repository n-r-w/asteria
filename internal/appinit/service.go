// Package appinit provides functions for initializing the application,
// including loading configuration and setting up LSP implementations.
package appinit

import (
	"github.com/n-r-w/asteria/internal/config"
	"github.com/n-r-w/asteria/internal/server"
	"github.com/n-r-w/asteria/internal/usecase/router"
)

// CreateDIContainer initializes the application components and
// returns a DI container with the initialized dependencies.
func CreateDIContainer(version string) (*DIContainer, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	lspImpls, closeFuncs, err := initLSP(cfg)
	if err != nil {
		return nil, err
	}

	routerSvc, err := router.New(lspImpls)
	if err != nil {
		return nil, err
	}

	return &DIContainer{
		Cfg:        cfg,
		LSPImpls:   lspImpls,
		CloseFuncs: closeFuncs,
		Router:     routerSvc,
		MCPServer:  server.New(version, routerSvc, cfg),
	}, nil
}
