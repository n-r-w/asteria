// Package appinit provides functions for initializing application components and LSP implementations.
package appinit

import (
	"errors"

	"github.com/n-r-w/asteria/internal/config"
	"github.com/n-r-w/asteria/internal/server"
	"github.com/n-r-w/asteria/internal/usecase/router"
)

// CreateDIContainer initializes application components and returns a DI container
// with the initialized dependencies.
func CreateDIContainer(version string, cfg *config.Config) (*DIContainer, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
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
