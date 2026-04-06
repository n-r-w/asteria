package server

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n-r-w/asteria/internal/config"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
	"github.com/stretchr/testify/require"
)

// TestRunReturnsNilOnContextCancellation verifies that Ctrl+C style shutdown is treated as a normal stop.
func TestRunReturnsNilOnContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	svc := &Service{ //nolint:exhaustruct // only mcpServer is relevant for context cancellation behavior
		mcpServer: mcp.NewServer(
			&mcp.Implementation{ //nolint:exhaustruct // optional SDK fields are irrelevant for shutdown test
				Name:    "test-server",
				Version: "v0.0.0",
			},
			nil,
		),
	}

	require.NoError(t, svc.Run(ctx))
}

// TestNewRegistersWorkspaceRootAsRequired proves that the published MCP schema requires workspace_root for all tools.
func TestNewRegistersWorkspaceRootAsRequired(t *testing.T) {
	t.Parallel()

	tools := listTestTools(t, newSchemaTestService())

	overviewTool := findToolByName(t, tools, "get_symbols_overview")
	overviewSchema, ok := overviewTool.InputSchema.(map[string]any)
	require.True(t, ok)
	require.ElementsMatch(t, []string{"file_path", "workspace_root"}, requiredPropertyNames(t, overviewSchema))

	findSymbolTool := findToolByName(t, tools, "find_symbol")
	findSymbolSchema, ok := findSymbolTool.InputSchema.(map[string]any)
	require.True(t, ok)
	require.ElementsMatch(t, []string{"symbol_query", "workspace_root"}, requiredPropertyNames(t, findSymbolSchema))

	findReferencesTool := findToolByName(t, tools, "find_referencing_symbols")
	findReferencesSchema, ok := findReferencesTool.InputSchema.(map[string]any)
	require.True(t, ok)
	require.ElementsMatch(
		t,
		[]string{"file_path", "symbol_path", "workspace_root"},
		requiredPropertyNames(t, findReferencesSchema),
	)
}

// TestNewRegistersSchemaPropertyNames keeps the published MCP schema aligned with the intended public contract.
func TestNewRegistersSchemaPropertyNames(t *testing.T) {
	t.Parallel()

	tools := listTestTools(t, newSchemaTestService())

	overviewTool := findToolByName(t, tools, "get_symbols_overview")
	require.ElementsMatch(t, []string{"depth", "file_path", "workspace_root"}, schemaPropertyNames(t, overviewTool))

	findSymbolTool := findToolByName(t, tools, "find_symbol")
	require.ElementsMatch(t, []string{
		"depth",
		"exclude_kinds",
		"include_body",
		"include_info",
		"include_kinds",
		"scope_path",
		"symbol_query",
		"substring_matching",
		"workspace_root",
	}, schemaPropertyNames(t, findSymbolTool))

	findReferencesTool := findToolByName(t, tools, "find_referencing_symbols")
	require.ElementsMatch(t, []string{"exclude_kinds", "file_path", "include_kinds", "symbol_path", "workspace_root"}, schemaPropertyNames(t, findReferencesTool))
}

// TestGetSymbolsOverviewToolRejectsOmittedWorkspaceRoot proves that the public MCP boundary rejects calls
// that omit the now-required workspace_root.
func TestGetSymbolsOverviewToolRejectsOmittedWorkspaceRoot(t *testing.T) {
	t.Parallel()

	clientSession := newTestClientSession(t, newSchemaTestService())

	_, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{ //nolint:exhaustruct // only tool name and arguments matter for the test
		Name: "get_symbols_overview",
		Arguments: map[string]any{
			"file_path": "internal/server/service.go",
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "workspace_root")
}

// TestFindSymbolToolRejectsOmittedWorkspaceRoot proves that the public MCP boundary rejects find_symbol calls
// that omit the now-required workspace_root.
func TestFindSymbolToolRejectsOmittedWorkspaceRoot(t *testing.T) {
	t.Parallel()

	clientSession := newTestClientSession(t, newSchemaTestService())

	_, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{ //nolint:exhaustruct // only tool name and arguments matter for the test
		Name: "find_symbol",
		Arguments: map[string]any{
			"symbol_query": "New",
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "workspace_root")
}

// TestFindReferencingSymbolsToolRejectsOmittedWorkspaceRoot proves that the public MCP boundary rejects
// find_referencing_symbols calls that omit the now-required workspace_root.
func TestFindReferencingSymbolsToolRejectsOmittedWorkspaceRoot(t *testing.T) {
	t.Parallel()

	clientSession := newTestClientSession(t, newSchemaTestService())

	_, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{ //nolint:exhaustruct // only tool name and arguments matter for the test
		Name: "find_referencing_symbols",
		Arguments: map[string]any{
			"file_path":   "internal/server/service.go",
			"symbol_path": "New",
		},
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "workspace_root")
}

// newSchemaTestService keeps schema and boundary tests focused on MCP behavior instead of repeated config literals.
func newSchemaTestService() *Service {
	return New("v0.0.0", nil, &config.Config{
		CacheRoot:              "",
		SystemPrompt:           "",
		GetSymbolsOverviewDesc: "",
		FindSymbolDesc:         "",
		FindReferencesDesc:     "",
		ToolTimeout:            10 * time.Second,
		ToolOutputMaxBytes:     0,
		Adapters:               cfgadapters.Config{},
	})
}

// newTestClientSession wires one in-memory MCP client to the service under test and registers cleanup.
func newTestClientSession(t *testing.T, svc *Service) *mcp.ClientSession {
	t.Helper()

	client := mcp.NewClient(&mcp.Implementation{ //nolint:exhaustruct // optional SDK fields are irrelevant for test transport setup
		Name:    "test-client",
		Version: "v0.0.0",
	}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := svc.mcpServer.Connect(t.Context(), serverTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, serverSession.Close())
	})

	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, clientSession.Close())
	})

	return clientSession
}

// listTestTools returns the registered tool set through one fully connected in-memory MCP session.
func listTestTools(t *testing.T, svc *Service) []*mcp.Tool {
	t.Helper()

	clientSession := newTestClientSession(t, svc)
	toolsResult, err := clientSession.ListTools(t.Context(), nil)
	require.NoError(t, err)

	return toolsResult.Tools
}

// findToolByName keeps schema assertions focused on one tool registration.
func findToolByName(t *testing.T, tools []*mcp.Tool, toolName string) *mcp.Tool {
	t.Helper()

	for _, tool := range tools {
		if tool.Name == toolName {
			return tool
		}
	}

	t.Fatalf("tool %q not found", toolName)

	return nil
}

// requiredPropertyNames converts one inferred JSON schema required list into plain strings.
func requiredPropertyNames(t *testing.T, schema map[string]any) []string {
	t.Helper()

	requiredRaw, ok := schema["required"]
	require.True(t, ok)

	requiredItems, ok := requiredRaw.([]any)
	require.True(t, ok)

	required := make([]string, 0, len(requiredItems))
	for _, item := range requiredItems {
		name, ok := item.(string)
		require.True(t, ok)
		required = append(required, name)
	}

	return required
}

// schemaPropertyNames returns one inferred JSON schema property list as plain strings.
func schemaPropertyNames(t *testing.T, tool *mcp.Tool) []string {
	t.Helper()

	schema, ok := tool.InputSchema.(map[string]any)
	require.True(t, ok)

	propertiesRaw, ok := schema["properties"]
	require.True(t, ok)

	properties, ok := propertiesRaw.(map[string]any)
	require.True(t, ok)

	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}

	return names
}
