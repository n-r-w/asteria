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
	if request != nil {
		release, acquireErr := s.rt.AcquireSession(ctx, request.WorkspaceRoot)
		if acquireErr != nil {
			return domain.GetSymbolsOverviewResult{}, acquireErr
		}
		defer release()
	}

	return s.std.GetSymbolsOverview(ctx, request)
}

// FindSymbol resolves canonical Markdown symbol paths while stdlsp keeps the shared search workflow.
func (s *Service) FindSymbol(
	ctx context.Context,
	request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	if request != nil {
		release, acquireErr := s.rt.AcquireSession(ctx, request.WorkspaceRoot)
		if acquireErr != nil {
			return domain.FindSymbolResult{}, acquireErr
		}
		defer release()
	}

	return s.std.FindSymbol(ctx, request)
}

// FindReferencingSymbols resolves Markdown symbol references while stdlsp keeps the shared reference workflow.
func (s *Service) FindReferencingSymbols(
	ctx context.Context,
	request *domain.FindReferencingSymbolsRequest,
) (domain.FindReferencingSymbolsResult, error) {
	if request != nil {
		release, acquireErr := s.rt.AcquireSession(ctx, request.WorkspaceRoot)
		if acquireErr != nil {
			return domain.FindReferencingSymbolsResult{}, acquireErr
		}
		defer release()
	}

	return s.std.FindReferencingSymbols(ctx, request)
}
