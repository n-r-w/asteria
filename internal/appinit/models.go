package appinit

import (
	"context"
	"errors"

	"github.com/n-r-w/asteria/internal/config"
	"github.com/n-r-w/asteria/internal/server"
	"github.com/n-r-w/asteria/internal/usecase/router"
)

// CloseFunc defines a function type for closing resources with context.
type CloseFunc func(ctx context.Context) error

// DIContainer holds the dependencies for the application.
type DIContainer struct {
	Cfg        *config.Config
	LSPImpls   []router.ILSP
	CloseFuncs []CloseFunc
	Router     *router.Service
	MCPServer  *server.Service
}

// Close releases adapter-owned resources so long-lived LSP child processes do not outlive the application.
func (di *DIContainer) Close(ctx context.Context) error {
	if di == nil {
		return nil
	}

	var closeErr error
	for _, closeFunc := range di.CloseFuncs {
		if closeFunc == nil {
			continue
		}

		closeErr = errors.Join(closeErr, closeFunc(ctx))
	}

	return closeErr
}
