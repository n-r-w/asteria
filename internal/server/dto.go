//nolint:lll // ok for tool descriptions in DTO tags
package server

// === get_symbols_overview ===

// getSymbolsOverviewInput is MCP input DTO for get_symbols_overview tool.
type getSymbolsOverviewInput struct {
	// WorkspaceRoot selects the workspace root used to resolve FilePath.
	WorkspaceRoot string `json:"workspace_root" jsonschema:"Required absolute workspace root directory used to resolve file_path."`
	// FilePath is the workspace-relative file path to inspect.
	FilePath string `json:"file_path" jsonschema:"Workspace-relative filesystem path to one file to inspect, relative to the selected workspace_root."`
	// Depth limits how deep child symbols should be returned.
	Depth int `json:"depth,omitempty" jsonschema:"Depth up to which descendants shall be retrieved (e.g. use 1 to also retrieve immediate children; for the case where the symbol is a class, this will return its methods). Default 0."`
}

// getSymbolsOverviewOutput is MCP output DTO for get_symbols_overview tool.
type getSymbolsOverviewOutput struct {
	// Groups contains one compact overview bucket per symbol kind.
	Groups []overviewKindGroupDTO `json:"groups"`
	// ReturnedPercent is the approximate percentage of logical result objects returned when the response is truncated.
	ReturnedPercent int `json:"returned_percent,omitempty"`
}

// overviewKindGroupDTO is transport model for one grouped get_symbols_overview bucket.
type overviewKindGroupDTO struct {
	// Kind is the stable LSP symbol kind code for all symbols in this group.
	Kind int `json:"kind"`
	// Symbols contains all overview entries that share the same kind.
	Symbols []overviewGroupSymbolDTO `json:"symbols"`
}

// overviewGroupSymbolDTO is transport model for one symbol entry inside one grouped overview kind bucket.
type overviewGroupSymbolDTO struct {
	// Path is the slash-delimited symbol path. Duplicate same-name siblings add '@line:character' to the last segment.
	Path string `json:"path"`
	// Range is the 0-based inclusive line range of the symbol, formatted as "start-end" or "start".
	Range string `json:"range"`
}

// === find_symbol ===

// findSymbolInput is MCP input DTO for find_symbol tool.
type findSymbolInput struct {
	// WorkspaceRoot selects the workspace root used to resolve ScopePath.
	WorkspaceRoot string `json:"workspace_root" jsonschema:"Required absolute workspace root directory used to resolve scope_path."`
	// SymbolQuery is the requested symbol query.
	SymbolQuery string `json:"symbol_query" jsonschema:"Symbol search query, not a filesystem path. Supports simple names, suffix lookup, and exact lookup with a leading '/'. Slash-separated segments mean parent/child nesting in the symbol tree, not a general semantic relation. Duplicate same-name siblings add '@line:character' to the last segment; reuse the returned path when you need one exact duplicate."`
	// ScopePath optionally narrows search scope to one workspace-relative file or directory.
	ScopePath string `json:"scope_path,omitempty" jsonschema:"Optional workspace-relative filesystem path to one file or directory that limits search, relative to the selected workspace_root."`
	// IncludeKinds restricts results to the provided symbol kinds.
	IncludeKinds []int `json:"include_kinds,omitempty" jsonschema:"List of LSP symbol kind integers to include. If not provided, all kinds are included."`
	// ExcludeKinds removes results matching the provided symbol kinds.
	ExcludeKinds []int `json:"exclude_kinds,omitempty" jsonschema:"List of LSP symbol kind integers to exclude. Takes precedence over include_kinds. If not provided, no kinds are excluded."`
	// Depth limits how deep child symbols should be returned.
	Depth int `json:"depth,omitempty" jsonschema:"Depth up to which descendants shall be retrieved (e.g. use 1 to also retrieve immediate children; for the case where the symbol is a class, this will return its methods). Depth expands descendants of matched symbols; it does not change how symbol_query is matched. Default 0. include_kinds and exclude_kinds apply to every returned symbol, including descendants, so filtered descendants are omitted."`
	// IncludeBody requests symbol body/source inclusion when supported.
	IncludeBody bool `json:"include_body,omitempty" jsonschema:"Whether to include the symbol's source code. Use judiciously."`
	// IncludeInfo requests symbol metadata inclusion when supported.
	IncludeInfo bool `json:"include_info,omitempty" jsonschema:"Whether to include additional info (hover-like, typically including docstring and signature), about the symbol (ignored if include_body is True). When depth > 0, descendants may include info too. Note: Depending on the language, this can be slow (e.g., C/C++)."`
	// SubstringMatching enables partial matching for the last path segment.
	SubstringMatching bool `json:"substring_matching,omitempty" jsonschema:"If True, use substring matching only for the last element of the pattern; earlier path segments must still match normally. For example, 'Foo/get' would match 'Foo/getValue' and 'Foo/getData'."`
}

// findSymbolOutput is MCP output DTO for find_symbol tool.
type findSymbolOutput struct {
	// Symbols contains matched symbol locations. Empty means no matches survived the requested scope and filters.
	Symbols []foundSymbolDTO `json:"symbols"`
	// ReturnedPercent is the approximate percentage of logical result objects returned when the response is truncated.
	ReturnedPercent int `json:"returned_percent,omitempty"`
}

// foundSymbolDTO is transport model for find_symbol responses.
type foundSymbolDTO struct {
	// Kind is the stable LSP symbol kind code for this symbol.
	Kind int `json:"kind"`
	// Body is the source code of the symbol, if requested.
	Body string `json:"body,omitempty"`
	// Info is the metadata of the symbol, if requested.
	Info string `json:"info,omitempty"`
	// Path is the slash-delimited symbol path. Duplicate same-name siblings add '@line:character' to the last segment.
	Path string `json:"path"`
	// File is the workspace-relative file path containing the symbol.
	File string `json:"file"`
	// Range is the 0-based inclusive line range of the symbol, formatted as "start-end" or "start".
	Range string `json:"range"`
}

// === find_referencing_symbols ===

// findReferencingSymbolsInput is MCP input DTO for find_referencing_symbols tool.
type findReferencingSymbolsInput struct {
	// WorkspaceRoot selects the workspace root used to resolve FilePath.
	WorkspaceRoot string `json:"workspace_root" jsonschema:"Required absolute workspace root directory used to resolve file_path."`
	// FilePath is the workspace-relative file path where the target symbol is declared.
	FilePath string `json:"file_path" jsonschema:"Workspace-relative filesystem path to the file where the target symbol is declared, relative to the selected workspace_root."`
	// SymbolPath identifies the target symbol inside FilePath.
	SymbolPath string `json:"symbol_path" jsonschema:"Symbol path inside the declaration file file_path, not a filesystem path. Supports suffix lookup and exact file-local lookup with a leading '/'. Slash-separated segments mean parent/child nesting in the symbol tree, not a general semantic relation. Duplicate same-name siblings add '@line:character' to the last segment; reuse the returned path when you need one exact duplicate. Use this to identify the declared target symbol whose usages should be found."`
	// IncludeKinds restricts results to the provided symbol kinds.
	IncludeKinds []int `json:"include_kinds,omitempty" jsonschema:"List of LSP symbol kind integers to include. If not provided, all kinds are included."`
	// ExcludeKinds removes results matching the provided symbol kinds.
	ExcludeKinds []int `json:"exclude_kinds,omitempty" jsonschema:"List of LSP symbol kind integers to exclude. Takes precedence over include_kinds. If not provided, no kinds are excluded."`
}

// findReferencingSymbolsOutput is MCP output DTO for find_referencing_symbols tool.
type findReferencingSymbolsOutput struct {
	// Files contains non-declaration referencing symbols grouped by file with one representative snippet per logical container.
	// Empty means the target symbol has no usages outside its declaration or no references survived the requested filters.
	Files []referencingFileDTO `json:"files"`
	// ReturnedPercent is the approximate percentage of logical result objects returned when the response is truncated.
	ReturnedPercent int `json:"returned_percent,omitempty"`
}

// referencingFileDTO groups referencing symbols by file so file paths do not repeat per symbol.
type referencingFileDTO struct {
	// File is the workspace-relative file path containing the referencing symbols.
	File string `json:"file"`
	// Symbols contains referencing symbols found in this file.
	Symbols []referencingSymbolDTO `json:"symbols"`
}

// referencingSymbolDTO is transport model for grouped find_referencing_symbols responses.
type referencingSymbolDTO struct {
	// Kind is the stable LSP symbol kind code for the referencing symbol.
	Kind int `json:"kind"`
	// Path is the slash-delimited symbol path. Duplicate same-name siblings add '@line:character' to the last segment.
	Path string `json:"path"`
	// Range is the 0-based inclusive line range of the returned code snippet, not of the exact reference token, formatted as "start-end" or "start".
	Range string `json:"range"`
	// Content is a short source snippet around the representative reference.
	Content string `json:"content"`
}
