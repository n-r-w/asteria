# Project Specific Rules and Information

## Project Overview
Symbolic(LSP) Search MCP Server. Designed for AI agents, not for humans.

## Goal
1. Provide LLM agents with the ability to efficiently and accurately navigate code, minimizing the number of tokens (file content reading).
2. CRITICAL: No unnecessary "noise" in responses to the client. Only information that helps the LLM agent understand where the needed symbol is located, what it represents, and how to reach it.

## Tech stack
1. go 1.26
2. `github.com/modelcontextprotocol/go-sdk` for MCP server implementation
3. `github.com/stretchr/testify` for tests
4. `go.uber.org/mock` (no custom mocks, `//go:generate` directives in interface files)
5. `github.com/caarlos0/env/v11` for loading configuration from environment variables
6. `log/slog` for logging (must use structured logging with context). Use global logger with context, instead of passing logger instances around. E.g. `slog.DebugContext`.

## Documentation

1. How to implement a new LSP adapter: `docs/lsp-server-implementation-guide.md`. MUST read before starting implementation.
2. Implementation of a similar service in python to identify patterns of working with various LSP adapters: `/Users/rvnikulenk/dev/nrw/serena/src/solidlsp/language_servers/`. No need to copy algorithms from there, it's only for borrowing ideas and practical examples. Use it as well when problems arise with implementation, especially with `initialize_params`.

## MCP Tools

This server implements following tools:
1. `get_symbols_overview`: high-level understanding of the code symbols in a file. Returns information containing symbols grouped by kind in a compact format.
2. `find_symbol`: retrieves information on all symbols/code entities (classes, methods, etc.) based on the given name path pattern. The returned symbol information can be used for edits or further queries.
3. `find_referencing_symbols`: Finds references to the symbol. Returns metadata and code snippets around each reference.

Tool descriptions:
- `internal/config/consts.go` - contains mcp system prompt (description for all tools) and individual tool descriptions
- `internal/server/dto.go` - contains MCP request and response models (DTOs) for each tool
- REMEMBER, ALL tool descriptions in `internal/config/consts.go` are intended for LLM and should be concise to save tokens,
while still providing enough information for the LLM to understand what each tool does. NO NEED to duplicate information in tool descriptions that is already present in the system prompt.

## Instructions
1. DON'T edit AGENTS.md and ifaceguard.cfg without DIRECT user request.
2. Maintain consistency of environment variables between `.env.example`, `.env`, Taskfile.yml, scripts, code, and documentation.

## Folder structure
1. `internal/adapters/lsp/servers/{lsp server name}` - LSP server implementation
2. `internal/adapters/lsp/servers/{lsp server name}/testdata` - test data for integration tests
3. `internal/adapters/lsp/runtimelsp` - LSP session management
4. `internal/adapters/lsp/stdlsp` - symbolic-search coordination for standard LSP adapters
5. `internal/server` - MCP server implementation
6. `internal/usecase/router` - routing mcp requests to specific LSP, according file extensions

## Architecture
1. Specific implementations of LSP servers should be as thin as possible and reuse helper packages `runtimelsp` and `stdlsp` where possible.
2. At the same time, it should be possible to implement a completely custom adapter that implements the `router.ILSP` and `server.ILSPServer` interfaces without using `runtimelsp` and `stdlsp`. This is necessary to ensure architectural flexibility and the ability to support a wide range of LSP servers in the future.

## Coding rules
1. All interfaces MUST be prefixed with uppercase `I` letter
2. For single package:
    1) Interfaces should be in file `interfaces.go`
    2) Main package struct and its constructor should be in `service.go` or `client.go`
    3) Internal helper structures (request/response models, etc.) should be located in the models.go file.
    4) DTO should be in dto.go (structs with tag `json`, `yaml`, etc.)
    5) Configuration related code should be in config.go (using `github.com/caarlos0/env/v11`)
    6) Internal errors should be in errors.go file
    7) Internal constants should be in consts.go file
    8) Mock generation commands should be in `interfaces.go`
3. ALL DTOs MUST be not exported.
4. Use `task lint`, `task test` and `task itest` to check code before completing changes.
5. Run `task fix` after making batch changes to improve code quality.
6. MUST avoid "test spam" - creating tests that are not meaningful and do not add value to the project.
7. MUST NOT use deprecated params `protocol.InitializeParams.RootPath/RootURI`, use `protocol.InitializeParams.WorkspaceFolders` instead. Some legacy LSP implementations may have exceptions.
8. When implementing LSP servers, check the work primarily with integration tests, as units can lie.

## Testing rules
1. Use `t.Context()` instead of `context.Background()`
2. Use `go.uber.org/mock` for mocks. Custom mocks are FORBIDDEN. Use `//go:generate` directives in interface files to generate mocks as needed.
3. Use `github.com/stretchr/testify`
4. Use `testify/suite`
5. Integration fixtures and tests using them MUST cover ALL language features, not just basic constructs.