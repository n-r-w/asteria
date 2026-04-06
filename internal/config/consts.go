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
	The tool resolves one unique target symbol by suffix lookup or exact lookup with a leading '/'.
	It does not support substring matching and reports ambiguity by listing matching candidates.
	🚨 MUST be preferred over ANY other search methods for locating code references. 🚨`

	systemPrompt = `🚨 CORE PRINCIPLES:
		1. MUST use symbolic tools for all code analysis
		2. MUST read only the minimum code necessary to complete each task
		3. SHOULD NOT read the same content multiple times with different tools
		4. MUST avoid reading entire files when specific symbols will suffice
		5. SHOULD use symbolic tools to get overviews before reading detailed code
		6. MUST prefer symbol-based tools over file-based when it is possible
		7. All returned range values are 0-based and inclusive, formatted as 'start-end' or 'start'.
		8. If the response is truncated, it also returns integer 'returned_percent' with approximate percentage of logical result objects returned.		

    🚨 CODE ANALYSIS WORKFLOW:
		1. MUST first use 'get_symbols_overview' to understand file structure
		2. SHOULD use 'find_symbol' for targeted code exploration
		3. Generic one-segment queries such as 'New', 'Run', 'Handle', 'Serve', 'Test', etc. can return many matches. Narrow them with 'scope_path' and kind filters before using 'find_symbol'
		4. When the parent symbol is known, prefer exact lookup with a leading '/'
		5. When the file or package is still unknown, use 'get_symbols_overview' first and only then call 'find_symbol'
		6. MUST use 'find_referencing_symbols' to understand symbol usage before modifications
		7. If symbol search tools return no results, then use file-based search tools as a last resort

	🚨 ARGUMENT GLOSSARY:
		1. 'workspace_root' = required absolute workspace root directory. Keep 'file_path' and 'scope_path' relative to it.
		1. 'file_path' = workspace-relative filesystem path to one file. Never a directory. Example: 'pkg/users/service.go'
		2. 'scope_path' = optional workspace-relative filesystem path to one file or directory that limits search. Examples: 'pkg/users', 'pkg/users/service.go'
		3. 'symbol_query' = symbol search query, not a filesystem path. Examples: 'GetUser', 'UserService/GetUser', '/UserService/GetUser'. Duplicate same-name siblings add '@line:character' to the last segment.
		4. 'symbol_path' = symbol path inside 'file_path', not a filesystem path. With file_path='pkg/users/service.go': 'GetUser', 'UserService/GetUser', '/UserService/GetUser'. Duplicate same-name siblings add '@line:character' to the last segment.

	🚨 ARGUMENT RULES:
		1. A leading '/' in 'symbol_query' or 'symbol_path' means exact symbol lookup, not an absolute filesystem path
		2. Never pass a filesystem path into 'symbol_query' or 'symbol_path'
		3. Never pass a symbol value into 'file_path' or 'scope_path'
		4. The caller must always provide 'workspace_root'
		5. For 'find_referencing_symbols', 'file_path' must be the file where the target symbol is declared, not a file where it is only referenced
		6. Keep 'file_path' and 'symbol_path' from the same declaration. Don't mix 'file_path' from one match/example with 'symbol_path' from another
		7. When overview or symbol search returns a duplicate same-name sibling path with '@line:character', reuse that exact suffix for exact lookup
		8. Use 'A/B' only when symbol 'B' is actually nested under 'A' in the symbol tree
		9. 'depth' expands descendants of matched symbols. It does not change how 'symbol_query' is matched

	🚨 TOOL SELECTION EXAMPLES:
		1. "List all functions in 'auth.go'" -> 'get_symbols_overview'
		2. "Find all usage of 'AuthenticateUser' function" -> 'find_referencing_symbols'
		3. "Find the implementation of 'AuthenticateUser' function" -> 'find_symbol'
		4. "I need to read 'repo.go' file" -> 'get_symbols_overview' -> read relevant parts of the file
		5. "Find constructor 'New' in the server package" -> 'find_symbol' with 'scope_path' narrowed to that package
		6. "Find method 'New' when the owner type is already known" -> 'find_symbol' with exact query like '/Service/New'

	🚨 Supported Languages:
		1. Golang
		2. TypeScript/JavaScript
		3. Python
		4. Rust
		5. C/C++
		6. PHP
		7. Markdown
	
	🚨 LSP Symbol Kinds:
		File = 1;
		Module = 2;
		Namespace = 3;
		Package = 4;
		Class = 5;
		Method = 6;
		Property = 7;
		Field = 8;
		Constructor = 9;
		Enum = 10;
		Interface = 11;
		Function = 12;
		Variable = 13;
		Constant = 14;
		String = 15;
		Number = 16;
		Boolean = 17;
		Array = 18;
		Object = 19;
		Key = 20;
		Null = 21;
		EnumMember = 22;
		Struct = 23;
		Event = 24;
		Operator = 25;
		TypeParameter = 26;`
)
