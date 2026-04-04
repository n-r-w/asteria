# Asteria MCP

Asteria is an MCP server for symbolic code search over multiple language servers.

## Requirements

- Go 1.26
- Supported language servers installed in `PATH`
- For C and C++, install `clangd`

## Configuration

- `ASTERIAMCP_CACHE_ROOT` optionally overrides the managed cache root for adapter artifacts.
- When `ASTERIAMCP_CACHE_ROOT` is unset, Asteria uses `<os user cache dir>/asteria/cache`.
- `ASTERIAMCP_TOOL_TIMEOUT` optionally overrides the global timeout for one MCP tool call. The default is `10s`.

## Public tool contract

All public MCP tools require `workspace_root`.

- `get_symbols_overview`
- `find_symbol`
- `find_referencing_symbols`

`workspace_root` must be an absolute path to the workspace root used for path resolution.

Relative paths such as `file_path` and `scope_path` are resolved against `workspace_root`.

## Verification

Use these project tasks before finishing changes:

- `task lint`
- `task test`
- `task itest`
- `task build`