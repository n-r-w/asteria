# ✦ Asteria MCP

An MCP server for symbolic code search over multiple language servers.

Asteria was created as a lightweight alternative to [Serena](https://github.com/oraios/serena). It only borrows the idea of exposing three symbolic search tools - `get_symbols_overview`, `find_symbol`, and `find_referencing_symbols` - and does not try to replicate Serena's broader MCP toolkit.

The server exposes a small, stable tool surface for agents that need to inspect code with minimal file reads.

> WARNING: ⚠️ LSP servers are required.
> Asteria does **not** install or manage LSP servers for you.
> If the required language server is not found in `PATH`, the corresponding adapter fails on the first request.
> See [Language server installation](README.md#language-server-installation) for installation commands.

## Project status

- Project is in early development and should be considered alpha software.
- Server has been tested on macOS and Linux (Ubuntu).
- Windows is not supported because many LSP servers work poorly on it.

## Symbolic search tools

- `get_symbols_overview`: returns a compact overview of symbols in one file, grouped by LSP symbol kind
- `find_symbol`: finds symbols by path-like query across a workspace, file, or directory
- `find_referencing_symbols`: finds non-declaration usages of a symbol and groups them by file and logical container

No special prompts are required. In most cases, LLM will prefer symbolic searches in accordance with its own asteria prompt.

## The following languages ​​are currently supported

| Language | File extensions | Required executable |
| --- | --- | --- |
| Go | `.go` | `gopls` |
| TypeScript / JavaScript | `.ts`, `.tsx`, `.js`, `.jsx`, `.mts`, `.cts`, `.mjs`, `.cjs` | `typescript-language-server` |
| Markdown | `.md`, `.markdown` | `marksman` |
| Python | `.py`, `.pyi` | `basedpyright-langserver` |
| C / C++ | `.c`, `.cc`, `.cpp`, `.cxx`, `.h`, `.hpp`, `.cppm`, ... | `clangd` |
| PHP | `.php` | `phpactor` |
| Rust | `.rs` | `rust-analyzer` |

Adding a new LSP server: see [Adding a new LSP server](README.md#adding-a-new-lsp-server).

## LSP runtime requirements.
 
- Install the language servers you need.
- See [Language server installation](README.md#language-server-installation) for installation commands.
- Make sure they are available in `PATH` for the MCP client process.

## Asteria Installation

### Binary Releases

Prebuilt archives are produced for:

- macOS `amd64`
- macOS `arm64`
- Linux `amd64`
- Linux `arm64`

Download the appropriate archive from [GitHub Releases](https://github.com/n-r-w/asteria/releases).

### Homebrew

```bash
brew install --cask n-r-w/homebrew-tap/asteria-mcp
```

### Build from Source

Requirements:

- Go `1.26`

```bash
go build -o asteria-mcp ./cmd/asteria-mcp
```

or use Task:

```bash
task build
```

`task build` produces `bin/asteria-mcp`.

## Language server installation

Use the commands that match the languages you want to analyze.

### Go

- Executable: `gopls`
- macOS / Linux:

```bash
go install golang.org/x/tools/gopls@latest
```

Source: <https://go.dev/gopls/>

### TypeScript and JavaScript

- Executable: `typescript-language-server`
- macOS / Linux:

```bash
npm install -g typescript-language-server typescript
```

Source: <https://github.com/typescript-language-server/typescript-language-server>

### Markdown

- Executable: `marksman`
- macOS:

```bash
brew install marksman
```

- Linux:
	- If you use Homebrew, the same command as on macOS works.
	- Otherwise use the official binary release and place `marksman` in `PATH`.

Source: <https://github.com/artempyanykh/marksman/blob/main/docs/install.md>

### Python

- Executable: `basedpyright-langserver`
- macOS / Linux:

```bash
uv tool install basedpyright
```

- Alternative:

```bash
pip install basedpyright
```

Use this alternative on macOS and Linux if you prefer `pip` over `uv`.

Both commands install the `basedpyright` CLI and the `basedpyright-langserver` script.

Source: <https://docs.basedpyright.com/dev/installation/command-line-and-language-server/>

### C and C++

- Executable: `clangd`
- macOS:

```bash
brew install llvm
```

Homebrew installs `llvm` as a keg-only formula. If `command -v clangd` still returns nothing after installation, add `$(brew --prefix llvm)/bin` to your `PATH`.

- Linux:

```bash
sudo apt-get install clangd-12
sudo update-alternatives --install /usr/bin/clangd clangd /usr/bin/clangd-12 100
```

On non-Debian distributions, use the package manager or the official release binaries.

Source: <https://clangd.llvm.org/installation.html>

### PHP

- Executable: `phpactor`
- Requirements: PHP `8.1+`
- macOS / Linux:

```bash
curl -Lo phpactor.phar https://github.com/phpactor/phpactor/releases/latest/download/phpactor.phar
chmod a+x phpactor.phar
mv phpactor.phar ~/.local/bin/phpactor
```

Make sure `~/.local/bin` is in `PATH`.

Source: <https://phpactor.readthedocs.io/en/master/usage/standalone.html>

### Rust

- Executable: `rust-analyzer`
- macOS / Linux:

```bash
rustup component add rust-analyzer
rustup component add rust-src
```

Alternative on macOS:

```bash
brew install rust-analyzer
```

Source: <https://rust-analyzer.github.io/book/installation.html>

## Adding a new LSP server

Add each adapter under `internal/adapters/lsp/servers/<server-name>/`.

In most cases, the adapter should stay thin and reuse shared packages:

- `internal/adapters/lsp/runtimelsp` - LSP process lifecycle, session reuse, and workspace isolation
- `internal/adapters/lsp/stdlsp` - shared implementation for overview, symbol search, and reference search
- `internal/adapters/lsp/helpers` - common document and workspace utilities

There are two supported approaches:

- **Standard adapter** - use this when the language server follows standard LSP behavior closely enough for `stdlsp`
- **Custom adapter** - use this when the server needs its own symbol or reference workflow

High-level flow:

1. Create the adapter package.
2. Implement `router.ILSP` and `server.ILSP`.
3. Register the adapter in `internal/appinit/lsp.go`.
4. Add unit and integration tests with fixtures in `testdata/`.
5. Run `task lint`, `task test`, and `task itest`.

For the full implementation rules, package layout, multi-workspace behavior, cache requirements, and testing checklist, read [LSP Server Implementation Guide](docs/lsp-server-implementation-guide.md).

## Environment variables

- `ASTERIAMCP_CACHE_ROOT` (optional, default: `<os-user-cache-dir>/asteria/cache`)
	- Absolute path for managed adapter artifacts.

- `ASTERIAMCP_SYSTEM_PROMPT` (optional, default: built-in server instructions)
	- Overrides MCP server instructions.

- `ASTERIAMCP_GET_SYMBOLS_OVERVIEW_DESC` (optional, default: built-in `get_symbols_overview` description)
	- Overrides the MCP tool description.

- `ASTERIAMCP_FIND_SYMBOL_DESC` (optional, default: built-in `find_symbol` description)
	- Overrides the MCP tool description.

- `ASTERIAMCP_FIND_REFERENCES_DESC` (optional, default: built-in `find_referencing_symbols` description)
	- Overrides the MCP tool description.

- `ASTERIAMCP_TOOL_TIMEOUT` (optional, default: `360s`)
	- Global timeout for one MCP tool call.
	- Must be greater than `0`.

- `ASTERIAMCP_TOOL_OUTPUT_MAX_BYTES` (optional, default: `32768`)
	- Maximum serialized JSON size for one tool response.
	- Must be greater than `0`.

- `ASTERIAMCP_GOPLS_BUILD_FLAGS` (optional)
	- `gopls` build flags separated by `;`.
	- Example: `-tags=featurex;-tags=integration`

- `ASTERIAMCP_GOPLS_ENV` (optional)
	- `gopls` environment entries separated by `;`.
	- Example: `GOFLAGS=-tags=featurex;GOOS=linux`

- `ASTERIAMCP_RUST_ANALYZER_WORKSPACE_SYMBOL_SEARCH_LIMIT` (optional, default: `128`)
	- rust-analyzer workspace symbol search limit.

- `ASTERIAMCP_RUST_ANALYZER_STARTUP_READY_TIMEOUT` (optional, default: `30s`)
	- How long Asteria waits for rust-analyzer startup readiness.

You can start from `.env.example` if you want to keep local configuration in an env file.

## Client configuration examples

### Claude Code

```bash
claude mcp add -s user --transport stdio asteria /path/to/asteria-mcp
```

### VS Code, RooCode, and similar clients

```json
"asteria": {
	"command": "/path/to/asteria-mcp"
}
```

Notes:

- The `command` must point to the built executable.
- Asteria uses stdio transport.
- Language server binaries must be visible in the environment of the MCP client process.

## Operational notes

- Asteria creates one LSP session per normalized `workspace_root`.
- Different workspace roots are isolated from each other.
- Managed adapter artifacts are kept under the configured cache root, not inside the analyzed repository.
- The server communicates over stdio and starts language servers lazily on the first request.