package domain

const (
	// ToolNameGetSymbolsOverview identifies the symbols overview operation across all layers.
	ToolNameGetSymbolsOverview = "get_symbols_overview"
	// ToolNameFindSymbol identifies the symbol lookup operation across all layers.
	ToolNameFindSymbol = "find_symbol"
	// ToolNameFindReferencingSymbols identifies the symbol references operation across all layers.
	ToolNameFindReferencingSymbols = "find_referencing_symbols"
)

// SearchStoppedByTimeout is the warning reason for the files and file extensions that are not searched because a search
// stopped at the tool timeout.
const SearchStoppedByTimeout = "search stopped by timeout"

// ServerName is the name of the MCP server.
const ServerName = "asteria_mcp"
