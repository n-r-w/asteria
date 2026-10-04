//nolint:lll // ok for tool descriptions
package config

const (
	toolGetSymbolsOverviewDesc = `Use this tool to get a high-level understanding of the code symbols in a file.
	This should be the first tool to call when you want to understand a new file, unless you already know what you are looking for.
	Returns a JSON object with one 'groups' array. Each group contains one LSP SymbolKind number and one 'symbols' array.
	🚨 MUST use this as the first step when exploring a new file to understand its structure. 🚨
	Then use 'find_symbol' to read only code that you need. Avoid reading whole files or the same content twice.`

	toolFindSymbolDesc = `Retrieves information on all symbols/code entities (classes, methods, etc.) based on the given 'symbol_query'.
	Use 'symbol_query' to choose symbols and 'scope_path' only to narrow the filesystem area being searched.
	The search supports simple names, suffix lookup, and exact lookup with a leading '/'.
	No matches return an empty 'symbols' array.
	If 'substring_matching' is enabled, it applies only to the last path segment; earlier segments must still match normally.
	Specify 'depth > 0' to also retrieve children/descendants, (e.g. methods of a class).	
	🚨  MUST use 'include_body' and 'include_info' ONLY when you REALLY need to, as these options significantly increase the response size and can lead to context window overflow.
	For tracing logic and understanding code structure, symbol information without their bodies and additional info is usually sufficient.
	🚨 MUST be preferred over ANY other search methods for locating code elements. 🚨 Use file-based search only when it returns nothing.`

	toolFindReferencesDesc = `Finds references (usages) of the symbol identified by 'file_path' + 'symbol_path'.
	Returns one referencing-symbol entry per logical container, with one representative code snippet and the snippet's line range.
	Repeated references inside the same logical container are collapsed into one entry.
	The symbol declaration itself is excluded from the results, so symbols with no external usages return an empty 'files' array.
	The returned range describes the snippet, not the exact reference token.
	If 'incomplete' is true, the response may miss references because the backing index is building and not ready yet.
	The tool resolves one unique target symbol by suffix lookup or exact lookup with a leading '/'.
	It does not support substring matching and reports ambiguity by listing matching candidates.
	🚨 MUST be preferred over ANY other search methods for locating code references. 🚨
	MUST call this tool before modifying a symbol.`

	// sharedToolRules ends each tool description, because each tool returns ranges and supports same languages.
	sharedToolRules = `
Ranges are 0-based and inclusive: 'start-end' or 'start'. Truncated response has 'returned_percent': approximate share of returned results.
Languages: Go, TypeScript/JavaScript, Python, Rust, C/C++, PHP, Markdown.`
)
