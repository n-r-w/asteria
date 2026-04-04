// Package server contains MCP transport adapter and tool handlers.
package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/n-r-w/asteria/internal/config"
	"github.com/n-r-w/asteria/internal/domain"
)

// Service registers MCP tools and maps DTOs to symbolic search use-cases.
type Service struct {
	// cfg contains server configuration, including system prompt and tool descriptions.
	cfg *config.Config
	// mcpServer is the MCP server runtime instance used to register tools and run the server.
	mcpServer *mcp.Server
	// search executes symbolic search use-cases for tool handlers.
	search ILSP
}

// New constructs MCP server.
func New(serverVersion string, search ILSP, cfg *config.Config) *Service {
	mcpServer := mcp.NewServer(
		&mcp.Implementation{ //nolint:exhaustruct // optional fields use defaults
			Name:    domain.ServerName,
			Version: serverVersion,
			Title:   domain.ServerName,
		},
		//nolint:exhaustruct // optional fields use defaults
		&mcp.ServerOptions{
			Instructions: cfg.SystemPrompt,
		},
	)

	svc := &Service{
		mcpServer: mcpServer,
		search:    search,
		cfg:       cfg,
	}

	svc.register()

	return svc
}

// Run starts the MCP server with stdio transport.
// Parameters:
//   - ctx: server lifecycle context.
func (s *Service) Run(ctx context.Context) error {
	if err := s.mcpServer.Run(ctx, &mcp.StdioTransport{}); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}

		return fmt.Errorf("server run failed: %w", err)
	}

	return nil
}

// register adds symbolic search tools to MCP server runtime.
// Parameters:
//   - server: MCP server runtime being configured.
func (s *Service) register() {
	mcp.AddTool(s.mcpServer, &mcp.Tool{ //nolint:exhaustruct // external SDK
		Name:        domain.ToolNameGetSymbolsOverview,
		Description: s.cfg.GetSymbolsOverviewDesc,
	}, s.getSymbolsOverviewTool)

	mcp.AddTool(s.mcpServer, &mcp.Tool{ //nolint:exhaustruct // external SDK
		Name:        domain.ToolNameFindSymbol,
		Description: s.cfg.FindSymbolDesc,
	}, func(
		ctx context.Context,
		request *mcp.CallToolRequest,
		input findSymbolInput,
	) (*mcp.CallToolResult, findSymbolOutput, error) {
		return s.findSymbolTool(ctx, request, &input)
	})

	mcp.AddTool(s.mcpServer, &mcp.Tool{ //nolint:exhaustruct // external SDK
		Name:        domain.ToolNameFindReferencingSymbols,
		Description: s.cfg.FindReferencesDesc,
	}, func(
		ctx context.Context,
		request *mcp.CallToolRequest,
		input findReferencingSymbolsInput,
	) (*mcp.CallToolResult, findReferencingSymbolsOutput, error) {
		return s.findReferencingSymbolsTool(ctx, request, &input)
	})
}
