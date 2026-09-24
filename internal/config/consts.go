//nolint:lll // ok for tool descriptions
package config

const (
	toolGetSymbolsOverviewDesc = `Use this tool to get a high-level understanding of the code symbols in a file.
	This should be the first tool to call when you want to understand a new file, unless you already know what you are looking for.
	Returns a JSON object with one 'groups' array. Each group contains one numeric LSP symbol kind and one 'symbols' array.
	🚨 MUST use this as the first step when exploring a new file to understand its structure. 🚨`

	toolFindSymbolDesc = `Retrieves information on all symbols/code entities (classes, methods, etc.) based on the given 'symbol_query'.
	Use 'symbol_query' to choose symbols and 'scope_path' only to narrow the filesystem area being searched.
	The search supports simple names, suffix lookup, and exact lookup with a leading '/'.
	No matches return an empty 'symbols' array.
	If 'substring_matching' is enabled, it applies only to the last path segment; earlier segments must still match normally.
	Specify 'depth > 0' to also retrieve children/descendants, (e.g. methods of a class).	
	🚨  MUST use 'include_body' and 'include_info' ONLY when you REALLY need to, as these options significantly increase the response size and can lead to context window overflow.
	For tracing logic and understanding code structure, symbol information without their bodies and additional info is usually sufficient.
	🚨 MUST be preferred over ANY other search methods for locating code elements. 🚨`

	toolFindReferencesDesc = `Finds references (usages) of the symbol identified by 'file_path' + 'symbol_path'.
	Returns one referencing-symbol entry per logical container, with one representative code snippet and the snippet's line range.
	Repeated references inside the same logical container are collapsed into one entry.
	The symbol declaration itself is excluded from the results, so symbols with no external usages return an empty 'files' array.
	The returned range describes the snippet, not the exact reference token.
	If 'incomplete' is true, the response may miss references because the backing index is building and not ready yet.
	The tool resolves one unique target symbol by suffix lookup or exact lookup with a leading '/'.
	It does not support substring matching and reports ambiguity by listing matching candidates.
	🚨 MUST be preferred over ANY other search methods for locating code references. 🚨`

	systemPrompt = `🚨 CORE PRINCIPLES:
1. MUST use symbolic tools for code analysis. Use file-based search only when they return nothing
2. Read only the minimum code needed: 'get_symbols_overview' first, then 'find_symbol'. Avoid reading whole files or the same content twice
3. MUST use 'find_referencing_symbols' before modifying a symbol
4. Ranges are 0-based and inclusive: 'start-end' or 'start'. Truncated responses include 'returned_percent' (approximate share of returned results)

🚨 ARGUMENT RULES:
1. Symbol values ('symbol_query', 'symbol_path') and filesystem paths ('workspace_root', 'file_path', 'scope_path') are never interchangeable
2. Narrow generic names ('New', 'Run', 'Handle', ...) with 'scope_path' and kind filters. When the parent is known, use exact lookup like '/Service/New'
3. Take 'file_path' and 'symbol_path' from the same declaration. Reuse returned paths, including '@line:character', verbatim

Languages: Go, TypeScript/JavaScript, Python, Rust, C/C++, PHP, Markdown
Symbol kinds: 1 File, 2 Module, 3 Namespace, 4 Package, 5 Class, 6 Method, 7 Property, 8 Field, 9 Constructor, 10 Enum, 11 Interface, 12 Function, 13 Variable, 14 Constant, 15 String, 16 Number, 17 Boolean, 18 Array, 19 Object, 20 Key, 21 Null, 22 EnumMember, 23 Struct, 24 Event, 25 Operator, 26 TypeParameter`
)
