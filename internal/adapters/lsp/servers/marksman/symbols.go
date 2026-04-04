package lspmarksman

import (
	"context"

	"github.com/n-r-w/asteria/internal/domain"
)

// GetSymbolsOverview publishes canonical Markdown symbol paths while stdlsp keeps the shared overview workflow.
func (s *Service) GetSymbolsOverview(
	ctx context.Context,
	request *domain.GetSymbolsOverviewRequest,
) (domain.GetSymbolsOverviewResult, error) {
	return s.std.GetSymbolsOverview(ctx, request)
}

// FindSymbol resolves canonical Markdown symbol paths while stdlsp keeps the shared search workflow.
func (s *Service) FindSymbol(
	ctx context.Context,
	request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	return s.std.FindSymbol(ctx, request)
}

// FindReferencingSymbols resolves Markdown symbol references while stdlsp keeps the shared reference workflow.
func (s *Service) FindReferencingSymbols(
	ctx context.Context,
	request *domain.FindReferencingSymbolsRequest,
) (domain.FindReferencingSymbolsResult, error) {
	return s.std.FindReferencingSymbols(ctx, request)
}
