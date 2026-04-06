package router

import (
	"context"

	"github.com/n-r-w/asteria/internal/domain"
)

//go:generate mockgen -destination=interfaces_mock.go -package=router -source=interfaces.go

// ILSP defines use-case contract consumed by MCP tool handlers.
type ILSP interface {
	// GetSymbolsOverview returns a high-level overview of symbols in a file.
	GetSymbolsOverview(
		ctx context.Context, request *domain.GetSymbolsOverviewRequest) (domain.GetSymbolsOverviewResult, error)
	// FindSymbol finds symbols matching the pattern.
	FindSymbol(ctx context.Context, request *domain.FindSymbolRequest) (domain.FindSymbolResult, error)
	// FindReferencingSymbols returns references for a target symbol.
	FindReferencingSymbols(
		ctx context.Context, request *domain.FindReferencingSymbolsRequest) (domain.FindReferencingSymbolsResult, error)
	// Extensions - list of supported file extensions for this LSP implementation.
	Extensions() []string
}
