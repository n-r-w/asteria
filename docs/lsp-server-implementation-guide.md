# LSP Server Implementation Guide

CRITICAL: Before starting implementation, study how a similar LSP is done in `https://github.com/oraios/serena/tree/main/src/solidlsp/language_servers`. 
This will help avoid mistakes.

## Goal

A language adapter connects one concrete LSP server to Asteria's three symbolic-search operations:

- `get_symbols_overview`
- `find_symbol`
- `find_referencing_symbols`

The adapter hides language-server quirks behind the project's stable domain contract. The rest of the system must not need language-specific exceptions.

## What you are building

Put every new adapter in one package under:

- `internal/adapters/lsp/servers/<server-name>/`

Use this package layout:

- `service.go` — adapter struct, constructor, runtime wiring, `Extensions`, `Close`
- `symbols.go` — adapter-specific symbol normalization and operation overrides
- `consts.go` — supported extensions, language ID, other adapter constants
- tests and `testdata/` when integration fixtures are needed

Keep the adapter thin. Reuse shared packages when the server fits the standard workflow:

- `internal/adapters/lsp/runtimelsp` — starts and owns the LSP process
- `internal/adapters/lsp/stdlsp` — shared implementation for standard LSP symbol workflows
- `internal/adapters/lsp/helpers` — shared utilities for document lifecycle, workspace root validation, document path resolution, and directory file collection

Multi-workspace rules:

- every tool request must include `workspace_root`
- router resolves one effective root and forwards it to the adapter request
- `runtimelsp.Runtime` owns one session per normalized `workspace_root`
- one `session` still means one root, one process, one connection
- same-root concurrent callers must reuse that one session
- different-root callers must be able to run concurrently without sharing one session
- `stdlsp` must use `request.WorkspaceRoot`, not adapter instance state

## LSP server installation 

All LSP servers must be installed by the user. Asteria does not ship or manage LSP servers and expects them to be present in `PATH` or discoverable by other standard executable lookup rules. Make sure the target LSP server is installed and accessible in the environment where Asteria runs. 
Special environment variables for configuring paths to LSP servers are NOT NEEDED. 

If the LSP server is not installed, the adapter must:
- return an error in integration tests
- return an error when the adapter is called in real work, but only at the moment of the first request, not at application startup

## Two implementation modes

Choose exactly one mode for each adapter.

### Standard-LSP adapter

Use this mode when the language server fits the shared symbol and reference workflow closely enough.

Requirements:

- use `stdlsp` as the core implementation of symbol overview, symbol search, and reference search workflows
- usually use `runtimelsp` for process lifecycle and connection reuse
- keep adapter-specific code limited to hooks such as `BuildNamePath`, `IgnoreDir`, `WithRequestDocument`, request retries, query normalization, client capabilities, and `workspace/configuration` replies
- do not reimplement shared matching, grouping, or shaping logic inside the adapter package when `stdlsp` already covers it

If the adapter does not use `stdlsp` for its core workflow, it is not a standard adapter.

### Custom adapter

Use this mode when the language server does not fit the standard workflow or when `stdlsp` cannot model it correctly.

Requirements:

- implement `router.ILSP` directly in the adapter package
- own the full request/response workflow that is not delegated to `stdlsp`
- keep all language-specific behavior inside the adapter package
- still satisfy the same domain contract, registration rules, extension rules, and test requirements as standard adapters

Custom adapter rules:

- do not use `stdlsp` for the core symbol/reference workflow
- you may still use `runtimelsp` if only process/session lifecycle fits
- you may skip both helper packages entirely if lifecycle and transport also need custom handling

Using only `runtimelsp` does not make the adapter standard. The boundary is simple: if `stdlsp` owns the core workflow, the adapter is standard. If the adapter package owns the core workflow, the adapter is custom.

## Required contract

Every adapter must satisfy `router.ILSP` from `internal/usecase/router/interfaces.go`.

It must provide:

- `GetSymbolsOverview(ctx, request)`
- `FindSymbol(ctx, request)`
- `FindReferencingSymbols(ctx, request)`
- `Extensions() []string`

`Close(ctx) error` is not part of `router.ILSP`, but the concrete adapter service must still expose it because app startup registers one cleanup function per adapter instance.

Every adapter must include compile-time assertions for both interfaces:

```go
var (
	_ router.ILSP = (*Service)(nil)
	_ server.ILSP = (*Service)(nil)
)
```

## Hard rules

### 1. One extension belongs to one adapter

`router.Service` builds a single extension-to-adapter map. Supported extensions must therefore be:

- non-empty
- in `filepath.Ext` format with the leading dot, for example `.go` or `.ts`
- lowercase-friendly
- deduplicated after normalization, for example do not return both `.c` and `.C` from one adapter
- unique across all registered adapters

- Do not register the same file extension in two adapters.
- Do not return case-variant duplicates from one adapter either. Router normalizes
extensions with `strings.ToLower`, so `.c` and `.C` collide into the same key.

### 2. Keep all public behavior language-agnostic

The MCP layer expects one stable contract across languages. Normalize server-specific output so that:

- paths stay workspace-relative
- symbol kinds stay valid LSP symbol kinds
- symbol name paths are stable and searchable
- reference results are grouped and shaped exactly like the shared domain expects

If the raw language server returns odd names, unusual containers, or incomplete context, fix that inside the adapter.

### 3. Prefer canonical name paths

The most important adapter-specific job is name-path normalization.

Different LSP servers represent symbols differently. Examples:

- methods may include receiver syntax
- nested symbols may include package or module prefixes
- class members may be flattened or nested differently
- generic type parameters may appear in names

Convert raw server names into one canonical path format that users can query reliably. The query format must not depend on the server's raw naming style.

### 4. Keep server quirks local

If one LSP server needs special handling, keep it inside its package. Examples:

- temporary `didOpen` / `didClose` around one request
- custom client capabilities
- server-specific replies to `workspace/configuration`, for example `gopls` `buildFlags` or `env`
- filtering noisy directories
- retrying a request after path normalization
- post-processing raw symbol names or reference containers

Do not push language-specific conditions into router, server, domain, or tool-description layers unless the behavior is truly generic.

### 5. Reuse shared layers only in the standard-LSP mode

Use these rules:

1. If the server speaks standard LSP well enough, use `runtimelsp` + `stdlsp`.
2. Add only small adapter hooks for naming, directory filtering, request-scoped document opening, or similar quirks.
3. Switch to a custom adapter only when the shared workflow is not enough.

Specific adapters stay thin and shared behavior stays shared.

## Implementation pattern

### 1. Create the adapter package

Create a package under `internal/adapters/lsp/servers/<server-name>/`.

The main service holds only the dependencies required by the selected mode.

For a standard-LSP adapter, the service holds:

- one `*runtimelsp.Runtime`
- one `*stdlsp.Service` — either as a named field or as an embedded type

Use a named field (for example `std *stdlsp.Service`) when the adapter wraps every method with its own logic such as query normalization. Use embedding (`*stdlsp.Service`) when the adapter overrides only some methods and the rest delegate unchanged.

These services are multi-root. One adapter instance must serve requests for different roots.

For a custom adapter, the service holds only the dependencies required by its own workflow.

For a custom adapter, that may mean:

- no helper packages at all
- `runtimelsp` only
- a completely custom transport/session layer

### 2. Build the runtime only when the adapter uses `runtimelsp`

Build the runtime with `runtimelsp.New(...)` and a `RuntimeConfig`.

`RuntimeConfig` defines:

- `LSPConfig` — shared LSP process and session settings
- `BuildWorkspaceFolders` — optional workspace-folder builder

`LSPConfig` defines:

- command
- args
- server name
- optional shutdown timeout
- optional `ReplyConfiguration` callback for server-specific `workspace/configuration` responses
- optional client capabilities builder
- optional `FileWatch` config for runtime-managed `workspace/didChangeWatchedFiles`

The runtime starts lazily on first request.

Do not keep one fixed workspace root in `RuntimeConfig`. The runtime receives the concrete root from each request and reuses or creates a session for that normalized root.

The runtime must also preserve multi-root concurrency semantics:

- same-root concurrent callers reuse one session
- different-root callers keep separate sessions
- one bad request on one root must not poison successful requests on another root

### 2.1. Apply file watching only when the server needs on-disk change notifications

Use `LSPConfig.FileWatch` when the language server must stay fresh after create/change/delete events on files that are not currently open through `didOpen` / `didChange`. Typical signals:

- live symbol or reference results stay stale until server restart
- the server docs or live behavior show that it expects `workspace/didChangeWatchedFiles`
- build-graph files such as module or workspace manifests must trigger a workspace reload

Do not add a second watcher inside the adapter package. Reuse the shared runtime watcher.

`FileWatch` is the adapter hook that tells `runtimelsp` to do all of this:

- advertise watched-files client capability during `initialize`
- start one recursive `fsnotify` watcher for that workspace root
- send standard `workspace/didChangeWatchedFiles` notifications to the server

Keep language-specific rules inside the adapter package:

- `RelevantFile` decides which file changes matter for that language server
- `IgnoreDir` decides which directories to skip

Examples of relevant files:

- source files for that language, such as `.go` or `.php`
- workspace-definition files that change the server's build graph, such as `go.mod`, `go.sum`, `go.work`, or `go.work.sum`

Do not push these rules into `runtimelsp`, `router`, or other shared layers. The runtime owns the generic mechanism. The adapter owns the language-specific filter.

### 2.2. Keep adapter cache and generated artifacts out of the analyzed repository

Some language servers need persistent state or trigger external tools that write generated artifacts. Examples:

- server-specific indexes
- copied build metadata
- Cargo build artifacts triggered by the language server

Hard rules:

- do not create adapter-managed directories under `workspace_root`
- do not write generated files into the analyzed repository just because the server accepts a workspace-local path by default
- do not leak generated files outside Asteria's managed cache root either

Use the managed cache rooted at `cfg.CacheRoot`.

Implementation rules:

- pass `cfg.CacheRoot` from `internal/appinit/lsp.go` into every adapter that needs persistent state or generated artifacts
- validate and normalize it with `helpers.ResolveCacheRoot`
- derive one per-workspace, per-adapter cache directory with `helpers.AdapterCacheDir(cacheRoot, workspaceRoot, adapterName)`
- keep all adapter-managed artifacts under that directory

This gives you these properties automatically:

- different workspaces do not share one artifact directory
- different adapters do not collide inside one workspace cache
- deleting one workspace cache does not affect other roots

When the language server has its own cache or artifact path setting, point it at the managed cache directory instead of accepting the server's default. Examples:

- `phpactor` index path belongs under the managed adapter cache, not under `workspace_root/.phpactor`
- `clangd` managed compilation database copies belong under the managed adapter cache, not in the analyzed repository
- `rust-analyzer` Cargo artifacts must be redirected into the managed adapter cache, not the repository `target/`

When the language server does not expose a direct cache-path setting but invokes external tooling, use the server's env/config hooks to redirect those artifacts into the managed cache. For example, Cargo artifacts can be redirected with `CARGO_TARGET_DIR`.

If the only available server behavior still writes generated files into the repository and there is no clean redirect, stop and treat that as a design issue. Do not silently accept repository pollution for the sake of faster implementation.

### 3. Configure `stdlsp` only in the standard-LSP mode

If the server follows standard LSP semantics, create `stdlsp.New(...)` with:

- `Extensions`
- `EnsureConn`
- `OpenFileForDocumentSymbol` — set to `true` when the LSP server needs an open document buffer to return correct `textDocument/documentSymbol` results
- `OpenFileForReferenceWorkflow` — set to `true` when the LSP server needs an open document buffer to return correct `textDocument/references` and `textDocument/hover` results
- optional `WithRequestDocument` — required when either open-file flag above is `true`. Build it with `helpers.WithRequestDocument(languageIDFunc)` where `languageIDFunc` maps a file extension to the LSP language identifier
- optional `BuildNamePath` — adapter-specific name-path normalization
- optional `IgnoreDir` — adapter-specific directory filtering

When the adapter overrides `FindReferencingSymbols` with custom document-open logic (for example opening all files in the target directory before running the reference workflow), store the `WithRequestDocument` function as a service field for use in the override.

Use adapter hooks only for true language-specific behavior.

`EnsureConn` now receives `workspace_root`. `stdlsp.Config` must not keep one fixed root for the whole adapter lifetime.

If the adapter stops using `stdlsp` for the core workflow, move it to the custom-adapter mode instead of building a hybrid description.

### 4. Implement the service surface for the selected mode

The adapter service must:

- delegate directly to `stdlsp` in the standard-LSP mode when shared behavior is enough
- wrap `stdlsp` in the standard-LSP mode when the language needs normalization or retry logic
- implement the full workflow locally in the custom mode
- return supported extensions through `Extensions()`
- release process resources through `Close(ctx)`

For standard-LSP adapters, every incoming domain request must already contain an effective `WorkspaceRoot`. Fail fast if it is missing.

### 5. Register the adapter during app initialization

Add the new adapter to `internal/appinit/lsp.go`.

When the adapter needs configuration (for example build flags or environment variables), keep the config in `internal/config/adapters/...`. Parse env there, then map the parsed config to the adapter constructor in `internal/appinit/lsp.go`. The adapter package must not parse env on its own.

When the adapter needs managed cache placement, pass `cfg.CacheRoot` from app initialization instead of deriving cache locations inside the adapter from process cwd, temp folders, or the analyzed repository.

Not every adapter needs configuration. If the adapter has no configurable parameters, its constructor takes no config argument.

Registration has two parts:

- create the adapter instance
- append its `Close` method to the list of cleanup functions

If the adapter is not registered there, it will never receive requests.

Do not pass one fixed workspace root into the adapter constructor during app initialization. Adapter instances are root-agnostic and receive the effective root only through requests.

## Adapter responsibilities per operation

### `GetSymbolsOverview`

Return one stable, searchable tree overview for the target file. Ensure that:

- parent/child relationships are correct
- symbol kinds are correct
- returned paths use the canonical adapter format
- selection/body ranges are usable for later operations

### `FindSymbol`

Make symbols discoverable by canonical path, not by raw LSP naming accidents. Ensure that:

- exact path matching works
- substring matching still behaves correctly
- depth filtering still behaves correctly
- `include_body` and `include_info` still work when enabled
- failure to fetch optional `include_body` or `include_info` for one symbol does not fail the whole `find_symbol` request; return the symbol with the corresponding field left empty and log the adapter-local error

Normalize the user query before delegating to shared search when needed.

### `FindReferencingSymbols`

Keep reference lookup stable even when the underlying LSP has weak defaults. Ensure that:

- the target symbol can be resolved by canonical path
- references exclude declarations
- grouping is based on the containing symbol
- evidence snippets still point to the real use site
- cross-file and nested-workspace cases still work when the language server supports them

If the language server needs an active document buffer or extra context to resolve references or hover data correctly, handle that inside the adapter. Some language servers require opening not just the target file but all related files in the same directory before running the reference workflow. In that case, override `FindReferencingSymbols` in the adapter, collect the affected files with `helpers.CollectDirectoryFiles`, and use `WithRequestDocument` to open each file before delegating to the shared workflow.

## Testing checklist

Every new adapter must come with targeted tests.

### Unit tests

Add unit tests for adapter-specific behavior only. Cover these parts when they exist in the adapter:

- name-path normalization
- query normalization
- directory filtering
- error classification used for retry decisions
- request-scoped helper behavior that does not require a live server

### Integration tests

Add live integration tests when the adapter depends on real server behavior. Use stable fixtures under the adapter's `testdata/` folder.

At minimum, integration tests must verify:

- symbol overview returns expected symbols from fixture files
- `find_symbol` resolves canonical paths
- `find_referencing_symbols` groups references correctly
- concurrent same-root calls reuse one session when `runtimelsp` is used
- concurrent different-root calls keep separate sessions when `runtimelsp` is used
- invalid `workspace_root` or escaped paths do not break concurrent successful requests on other roots

Add extra integration coverage for real quirks of that language server, for example:

- nested workspace/module behavior
- hover info resolution
- transient open-document requirements
- raw `DocumentSymbol` shape assumptions

If the adapter uses `FileWatch`, add live integration coverage with a temp workspace copy. At minimum, prove that without restarting the server:

- a newly created relevant file becomes searchable
- a modified relevant file updates symbol results instead of serving stale data
- a deleted relevant file disappears from search results

Even when the adapter does not enable `FileWatch` initially, must add the same kind of live temp-workspace test when on-disk freshness is uncertain. Start with the test, not with the watcher. If the live server already picks up create/change/delete events without `workspace/didChangeWatchedFiles`, keep `FileWatch` disabled. Enable `FileWatch` only when the test proves that results stay stale without it.

Use `t.TempDir()` and copy tracked fixtures into it. Do not mutate files under `testdata/` directly.

## Quality bar

A new adapter is ready when all of the following are true:

- it implements the required interfaces
- it owns its language-specific quirks locally
- supported extensions are unique
- its canonical path rules are documented by tests
- it is registered in app initialization
- unit and integration tests cover all non-trivial adapter behavior
- `task lint`, `task test` and `task itest` pass

## Design summary

Use these ownership rules:

- shared mechanics belong in `runtimelsp`, `stdlsp`, and `helpers`
- language quirks belong in the adapter package
- routing belongs in `router`
- public MCP behavior must stay uniform across languages

Following that boundary keeps new language integrations limited to adapter work instead of framework changes.
