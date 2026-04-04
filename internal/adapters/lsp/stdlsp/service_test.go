package stdlsp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// TestRunRequestWithDocumentContextFallsBackToDirectCall proves that stdlsp keeps the direct flow
// when an adapter does not need any request-scoped document lifecycle.
func TestRunRequestWithDocumentContextFallsBackToDirectCall(t *testing.T) {
	t.Parallel()

	service := &Service{config: &Config{
		Extensions:                   nil,
		EnsureConn:                   nil,
		WithRequestDocument:          nil,
		OpenFileForDocumentSymbol:    false,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	}, cacheDisableWarnings: sync.Map{}}
	callCount := 0

	err := service.runRequestWithDocumentContext(t.Context(), nil, "/tmp/fixture.go", func(ctx context.Context) error {
		callCount++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 1, callCount)
}

// TestRunRequestWithDocumentContextUsesLifecycleHook proves that stdlsp lets adapters bracket
// one raw LSP request with request-scoped setup and cleanup.
func TestRunRequestWithDocumentContextUsesLifecycleHook(t *testing.T) {
	t.Parallel()

	steps := make([]string, 0, 3)
	service := &Service{config: &Config{
		Extensions: nil,
		EnsureConn: nil,
		WithRequestDocument: func(
			ctx context.Context,
			_ jsonrpc2.Conn,
			absolutePath string,
			run func(context.Context) error,
		) error {
			assert.Equal(t, "/tmp/fixture.go", absolutePath)
			steps = append(steps, "open")
			if err := run(ctx); err != nil {
				return err
			}
			steps = append(steps, "close")

			return nil
		},
		OpenFileForDocumentSymbol:    false,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	}, cacheDisableWarnings: sync.Map{}}

	err := service.runRequestWithDocumentContext(t.Context(), nil, "/tmp/fixture.go", func(ctx context.Context) error {
		steps = append(steps, "request")
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"open", "request", "close"}, steps)
}

// TestRequestRawDocumentSymbolsOpensDocumentWhenConfigured proves that documentSymbol requests can be
// wrapped in a request-scoped open document when the adapter opts into that behavior.
func TestRequestRawDocumentSymbolsOpensDocumentWhenConfigured(t *testing.T) {
	t.Parallel()

	assertRequestRawDocumentSymbolsLifecycle(t, true, 1)
}

// TestRequestRawDocumentSymbolsSkipsDocumentLifecycleWhenDisabled proves that adapters keep the old direct
// documentSymbol flow unless they explicitly enable request-scoped file opening.
func TestRequestRawDocumentSymbolsSkipsDocumentLifecycleWhenDisabled(t *testing.T) {
	t.Parallel()

	assertRequestRawDocumentSymbolsLifecycle(t, false, 0)
}

// assertRequestRawDocumentSymbolsLifecycle keeps the lifecycle toggle tests focused on the flag and expected calls.
func assertRequestRawDocumentSymbolsLifecycle(t *testing.T, openFileForDocumentSymbol bool, expectedLifecycleCalls int32) {
	t.Helper()

	workspaceRoot := t.TempDir()
	absolutePath := filepath.Join(workspaceRoot, "fixture.ts")
	require.NoError(t, os.WriteFile(absolutePath, []byte("export const fixture = 1;\n"), 0o600))

	conn, documentSymbolCalls := testDocumentSymbolConn(t, uri.File(absolutePath))
	var lifecycleCalls atomic.Int32
	service := &Service{config: &Config{
		Extensions: []string{".ts"},
		EnsureConn: func(context.Context, string) (jsonrpc2.Conn, error) {
			return conn, nil
		},
		WithRequestDocument: func(
			ctx context.Context,
			requestConn jsonrpc2.Conn,
			requestAbsolutePath string,
			run func(context.Context) error,
		) error {
			lifecycleCalls.Add(1)
			assert.Same(t, conn, requestConn)
			assert.Equal(t, absolutePath, requestAbsolutePath)

			return run(ctx)
		},
		OpenFileForDocumentSymbol:    openFileForDocumentSymbol,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	}, cacheDisableWarnings: sync.Map{}}

	cleanRelativePath, rawSymbols, err := service.requestRawDocumentSymbols(t.Context(), workspaceRoot, "fixture.ts")
	require.NoError(t, err)
	assert.Equal(t, "fixture.ts", cleanRelativePath)
	assert.Len(t, rawSymbols, 1)
	assert.Equal(t, expectedLifecycleCalls, lifecycleCalls.Load())
	assert.EqualValues(t, 1, documentSymbolCalls.Load())
}

// TestNewRejectsDocumentSymbolLifecycleWithoutHook proves that stdlsp refuses an incomplete configuration
// when documentSymbol requests must be wrapped with a temporary open document.
func TestNewRejectsDocumentSymbolLifecycleWithoutHook(t *testing.T) {
	t.Parallel()

	service, err := New(&Config{
		Extensions:                   []string{".ts"},
		EnsureConn:                   func(context.Context, string) (jsonrpc2.Conn, error) { return nil, errors.New("unused") },
		WithRequestDocument:          nil,
		OpenFileForDocumentSymbol:    true,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	})
	require.Nil(t, service)
	require.EqualError(t, err, "invalid stdlsp config: with request document callback is required when open_file_for_document_symbol is enabled")
}

// TestNewRejectsReferenceWorkflowLifecycleWithoutHook proves that stdlsp refuses to enable
// operation-scoped reference lifecycle without the shared request-document callback.
func TestNewRejectsReferenceWorkflowLifecycleWithoutHook(t *testing.T) {
	t.Parallel()

	ensureConnErr := errors.New("ensure conn should not run")
	service, err := New(&Config{
		Extensions:                   []string{".ts"},
		EnsureConn:                   func(context.Context, string) (jsonrpc2.Conn, error) { return nil, ensureConnErr },
		WithRequestDocument:          nil,
		OpenFileForDocumentSymbol:    false,
		OpenFileForReferenceWorkflow: true,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	})
	require.Nil(t, service)
	require.EqualError(t, err, "invalid stdlsp config: with request document callback is required when open_file_for_reference_workflow is enabled")
}

// TestNewRejectsPartialSymbolTreeCacheConfig proves that stdlsp refuses cache wiring that cannot decide both storage and metadata.
func TestNewRejectsPartialSymbolTreeCacheConfig(t *testing.T) {
	t.Parallel()

	service, err := New(&Config{
		Extensions:                   []string{".ts"},
		EnsureConn:                   func(context.Context, string) (jsonrpc2.Conn, error) { return nil, errors.New("unused") },
		WithRequestDocument:          nil,
		OpenFileForDocumentSymbol:    false,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache: &memorySymbolTreeCache{
			mu:         sync.Mutex{},
			entries:    map[string][]byte{},
			readCount:  0,
			writeCount: 0,
		},
		BuildSymbolTreeCacheMetadata: nil,
	})
	require.Nil(t, service)
	require.EqualError(t, err, "invalid stdlsp config: symbol tree cache and cache metadata builder must be configured together")
}

// TestGetSymbolsOverviewReusesCachedTreeAcrossRequests proves that overview requests now reuse the shared symbol-tree cache path.
func TestGetSymbolsOverviewReusesCachedTreeAcrossRequests(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	absolutePath := filepath.Join(workspaceRoot, "fixture.ts")
	require.NoError(t, os.WriteFile(absolutePath, []byte("export const fixture = 1;\n"), 0o600))
	normalizedAbsolutePath, err := filepath.EvalSymlinks(absolutePath)
	require.NoError(t, err)

	conn, documentSymbolCalls := testDocumentSymbolConn(t, uri.File(normalizedAbsolutePath))
	cacheStore := &memorySymbolTreeCache{mu: sync.Mutex{}, entries: map[string][]byte{}, readCount: 0, writeCount: 0}
	service := &Service{config: &Config{
		Extensions: []string{".ts"},
		EnsureConn: func(context.Context, string) (jsonrpc2.Conn, error) {
			return conn, nil
		},
		WithRequestDocument:          nil,
		OpenFileForDocumentSymbol:    false,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              cacheStore,
		BuildSymbolTreeCacheMetadata: fixedCacheMetadata,
	}, cacheDisableWarnings: sync.Map{}}

	firstResult, err := service.GetSymbolsOverview(t.Context(), &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.ts",
	})
	require.NoError(t, err)
	secondResult, err := service.GetSymbolsOverview(t.Context(), &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.ts",
	})
	require.NoError(t, err)

	assert.Equal(t, firstResult, secondResult)
	assert.EqualValues(t, 1, documentSymbolCalls.Load())
	assert.Equal(t, 1, cacheStore.writeCount)
	assert.Positive(t, cacheStore.readCount)
}

// TestFindReferencingSymbolsKeepsTargetOpenForWholeWorkflow proves that the optional outer request
// lifecycle keeps one target file open across documentSymbol and references requests.
func TestFindReferencingSymbolsKeepsTargetOpenForWholeWorkflow(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	absolutePath := filepath.Join(workspaceRoot, "fixture.ts")
	require.NoError(t, os.WriteFile(absolutePath, []byte("export function makeBucket() {}\n"), 0o600))
	normalizedAbsolutePath, err := filepath.EvalSymlinks(absolutePath)
	require.NoError(t, err)

	conn, workflowMethods, documentSymbolCalls, referenceCalls, openCalls, closeCalls := testReferenceWorkflowConn(
		t,
		uri.File(normalizedAbsolutePath),
	)
	service := &Service{config: &Config{
		Extensions:                   []string{".ts"},
		EnsureConn:                   func(context.Context, string) (jsonrpc2.Conn, error) { return conn, nil },
		WithRequestDocument:          helpers.WithRequestDocument(func(_ string) string { return "typescript" }),
		OpenFileForDocumentSymbol:    true,
		OpenFileForReferenceWorkflow: true,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	}, cacheDisableWarnings: sync.Map{}}

	result, err := service.FindReferencingSymbols(t.Context(), &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path:         "makeBucket",
			IncludeKinds: nil,
			ExcludeKinds: nil,
		},
		WorkspaceRoot: workspaceRoot,
		File:          "fixture.ts",
	})
	require.NoError(t, err)
	assert.Empty(t, result.Symbols)
	waitForWorkflowMethods(t, workflowMethods, []string{
		protocol.MethodTextDocumentDidOpen,
		protocol.MethodTextDocumentDocumentSymbol,
		protocol.MethodTextDocumentReferences,
		protocol.MethodTextDocumentDidClose,
	})
	require.Eventually(t, func() bool {
		return documentSymbolCalls.Load() == 1 &&
			referenceCalls.Load() == 1 &&
			openCalls.Load() == 1 &&
			closeCalls.Load() == 1
	}, time.Second, 10*time.Millisecond)
	assert.EqualValues(t, 1, documentSymbolCalls.Load())
	assert.EqualValues(t, 1, referenceCalls.Load())
	assert.EqualValues(t, 1, openCalls.Load())
	assert.EqualValues(t, 1, closeCalls.Load())
	assert.Equal(t, []string{
		protocol.MethodTextDocumentDidOpen,
		protocol.MethodTextDocumentDocumentSymbol,
		protocol.MethodTextDocumentReferences,
		protocol.MethodTextDocumentDidClose,
	}, workflowMethods.methods())
}

// TestFindReferencingSymbolsFallsBackToFileContainer proves that stdlsp keeps raw references when the
// referenced file has no documentSymbol container at the reference position.
func TestFindReferencingSymbolsFallsBackToFileContainer(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	targetAbsolutePath := filepath.Join(workspaceRoot, "fixture.ts")
	referenceAbsolutePath := filepath.Join(workspaceRoot, "imports.ts")
	require.NoError(t, os.WriteFile(targetAbsolutePath, []byte("export function makeBucket() {}\n"), 0o600))
	require.NoError(t, os.WriteFile(referenceAbsolutePath, []byte("import { makeBucket } from \"./fixture\"\nconst value = makeBucket\n"), 0o600))

	conn := testReferenceFallbackConn(t, uri.File(targetAbsolutePath), uri.File(referenceAbsolutePath))
	service := &Service{config: &Config{
		Extensions:                   []string{".ts"},
		EnsureConn:                   func(context.Context, string) (jsonrpc2.Conn, error) { return conn, nil },
		WithRequestDocument:          nil,
		OpenFileForDocumentSymbol:    false,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	}, cacheDisableWarnings: sync.Map{}}

	result, err := service.FindReferencingSymbols(t.Context(), &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path:         "makeBucket",
			IncludeKinds: nil,
			ExcludeKinds: nil,
		},
		WorkspaceRoot: workspaceRoot,
		File:          "fixture.ts",
	})
	require.NoError(t, err)
	require.Len(t, result.Symbols, 1)
	assert.Equal(t, domain.ReferencingSymbol{
		Kind:             int(protocol.SymbolKindFile),
		Path:             "imports",
		File:             "imports.ts",
		ContentStartLine: 0,
		ContentEndLine:   1,
		Content:          "import { makeBucket } from \"./fixture\"\nconst value = makeBucket",
	}, result.Symbols[0])
}

// TestBuildFoundSymbolSkipsOptionalHoverFailure proves that include_info stays best-effort
// and returns a symbol match even when the hover request fails.
func TestBuildFoundSymbolSkipsOptionalHoverFailure(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	absolutePath := filepath.Join(workspaceRoot, "fixture.go")
	require.NoError(t, os.WriteFile(absolutePath, []byte("package fixture\n"), 0o600))

	service := &Service{config: &Config{
		Extensions: []string{".go"},
		EnsureConn: func(context.Context, string) (jsonrpc2.Conn, error) {
			return nil, errors.New("hover unavailable")
		},
		WithRequestDocument:          nil,
		OpenFileForDocumentSymbol:    false,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	}, cacheDisableWarnings: sync.Map{}}
	node := &node{
		Kind:         int(protocol.SymbolKindFunction),
		NamePath:     "MakeBucket",
		RelativePath: "fixture.go",
		Range: protocol.Range{
			Start: protocol.Position{Line: 1, Character: 0},
			End:   protocol.Position{Line: 3, Character: 1},
		},
		SelectionRange: protocol.Range{
			Start: protocol.Position{Line: 1, Character: 5},
			End:   protocol.Position{Line: 1, Character: 15},
		},
		Children: nil,
	}
	request := &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:              "MakeBucket",
			IncludeKinds:      nil,
			ExcludeKinds:      nil,
			Depth:             0,
			IncludeBody:       false,
			IncludeInfo:       true,
			SubstringMatching: false,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "fixture.go",
	}

	foundSymbol := service.buildFoundSymbol(
		t.Context(),
		workspaceRoot,
		node,
		request,
		map[string]string{},
		map[string]string{},
	)
	assert.Equal(t, domain.FoundSymbol{
		Kind:      int(protocol.SymbolKindFunction),
		Body:      "",
		Info:      "",
		Path:      "MakeBucket",
		File:      "fixture.go",
		StartLine: 1,
		EndLine:   3,
	}, foundSymbol)
}

// TestBuildFoundSymbolSkipsOptionalBodyFailure proves that include_body stays best-effort
// and returns a symbol match even when the body cannot be read from disk.
func TestBuildFoundSymbolSkipsOptionalBodyFailure(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	service := &Service{config: &Config{
		Extensions:                   []string{".go"},
		EnsureConn:                   nil,
		WithRequestDocument:          nil,
		OpenFileForDocumentSymbol:    false,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	}, cacheDisableWarnings: sync.Map{}}
	node := &node{
		Kind:         int(protocol.SymbolKindFunction),
		NamePath:     "MakeBucket",
		RelativePath: "missing.go",
		Range: protocol.Range{
			Start: protocol.Position{Line: 1, Character: 0},
			End:   protocol.Position{Line: 3, Character: 1},
		},
		SelectionRange: protocol.Range{
			Start: protocol.Position{Line: 1, Character: 5},
			End:   protocol.Position{Line: 1, Character: 15},
		},
		Children: nil,
	}
	request := &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:              "MakeBucket",
			IncludeKinds:      nil,
			ExcludeKinds:      nil,
			Depth:             0,
			IncludeBody:       true,
			IncludeInfo:       false,
			SubstringMatching: false,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "missing.go",
	}

	foundSymbol := service.buildFoundSymbol(
		t.Context(),
		workspaceRoot,
		node,
		request,
		map[string]string{},
		map[string]string{},
	)
	assert.Equal(t, domain.FoundSymbol{
		Kind:      int(protocol.SymbolKindFunction),
		Body:      "",
		Info:      "",
		Path:      "MakeBucket",
		File:      "missing.go",
		StartLine: 1,
		EndLine:   3,
	}, foundSymbol)
}

// TestBuildFoundSymbolUsesRequestWorkspaceRoot proves that stdlsp reads optional body content from the request root, not service config.
func TestBuildFoundSymbolUsesRequestWorkspaceRoot(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	absolutePath := filepath.Join(workspaceRoot, "fixture.go")
	content := "package fixture\n\nfunc MakeBucket() string {\n\treturn \"ok\"\n}\n"
	require.NoError(t, os.WriteFile(absolutePath, []byte(content), 0o600))

	service := &Service{config: &Config{
		Extensions:                   []string{".go"},
		EnsureConn:                   nil,
		WithRequestDocument:          nil,
		OpenFileForDocumentSymbol:    false,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                nil,
		IgnoreDir:                    nil,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	}, cacheDisableWarnings: sync.Map{}}
	request := &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:              "MakeBucket",
			IncludeKinds:      nil,
			ExcludeKinds:      nil,
			Depth:             0,
			IncludeBody:       true,
			IncludeInfo:       false,
			SubstringMatching: false,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "fixture.go",
	}
	node := &node{
		Kind:         int(protocol.SymbolKindFunction),
		NamePath:     "MakeBucket",
		RelativePath: "fixture.go",
		Range: protocol.Range{
			Start: protocol.Position{Line: 2, Character: 0},
			End:   protocol.Position{Line: 4, Character: 1},
		},
		SelectionRange: protocol.Range{
			Start: protocol.Position{Line: 2, Character: 5},
			End:   protocol.Position{Line: 2, Character: 15},
		},
		Children: nil,
	}

	foundSymbol := service.buildFoundSymbol(
		t.Context(),
		workspaceRoot,
		node,
		request,
		map[string]string{},
		map[string]string{},
	)

	assert.Contains(t, foundSymbol.Body, "func MakeBucket() string")
	assert.Equal(t, workspaceRoot, request.WorkspaceRoot)
}

// testDocumentSymbolConn builds an in-memory JSON-RPC peer that answers one documentSymbol request with a stable payload.
func testDocumentSymbolConn(t *testing.T, expectedURI uri.URI) (jsonrpc2.Conn, *atomic.Int32) {
	t.Helper()

	serverSide, clientSide := net.Pipe()
	serverConn := jsonrpc2.NewConn(jsonrpc2.NewStream(serverSide))
	clientConn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	var documentSymbolCalls atomic.Int32

	serverConn.Go(t.Context(), func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		if req.Method() != protocol.MethodTextDocumentDocumentSymbol {
			return jsonrpc2.MethodNotFoundHandler(ctx, reply, req)
		}

		documentSymbolCalls.Add(1)

		var params protocol.DocumentSymbolParams
		err := json.Unmarshal(req.Params(), &params)
		if err != nil {
			t.Error(err)
			return err
		}
		assert.Equal(t, expectedURI, params.TextDocument.URI)

		return reply(ctx, []map[string]any{{"name": "fixtureSymbol"}}, nil)
	})
	clientConn.Go(t.Context(), jsonrpc2.MethodNotFoundHandler)

	t.Cleanup(func() {
		require.NoError(t, clientConn.Close())
		require.NoError(t, serverConn.Close())
		waitForConnDone(t, "clientConn", clientConn.Done())
		waitForConnDone(t, "serverConn", serverConn.Done())
	})

	return clientConn, &documentSymbolCalls
}

// testReferenceWorkflowConn builds an in-memory JSON-RPC peer that answers documentSymbol and references
// requests while counting lifecycle notifications for one target URI.
func testReferenceWorkflowConn(
	t *testing.T,
	expectedURI uri.URI,
) (jsonrpc2.Conn, *workflowMethodRecorder, *atomic.Int32, *atomic.Int32, *atomic.Int32, *atomic.Int32) {
	t.Helper()

	serverSide, clientSide := net.Pipe()
	serverConn := jsonrpc2.NewConn(jsonrpc2.NewStream(serverSide))
	clientConn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))
	recorder := &workflowMethodRecorder{}
	var documentSymbolCalls atomic.Int32
	var referenceCalls atomic.Int32
	var openCalls atomic.Int32
	var closeCalls atomic.Int32

	serverConn.Go(t.Context(), func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodTextDocumentDidOpen:
			recorder.add(req.Method())
			openCalls.Add(1)

			var params protocol.DidOpenTextDocumentParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return err
			}
			assert.Equal(t, expectedURI, params.TextDocument.URI)

			return nil
		case protocol.MethodTextDocumentDidClose:
			recorder.add(req.Method())
			closeCalls.Add(1)

			var params protocol.DidCloseTextDocumentParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return err
			}
			assert.Equal(t, expectedURI, params.TextDocument.URI)

			return nil
		case protocol.MethodTextDocumentDocumentSymbol:
			recorder.add(req.Method())
			documentSymbolCalls.Add(1)

			var params protocol.DocumentSymbolParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return err
			}
			assert.Equal(t, expectedURI, params.TextDocument.URI)

			return reply(ctx, []map[string]any{{
				"name": "makeBucket",
				"kind": int(protocol.SymbolKindFunction),
				"range": map[string]any{
					"start": map[string]any{"line": 0, "character": 0},
					"end":   map[string]any{"line": 0, "character": 30},
				},
				"selectionRange": map[string]any{
					"start": map[string]any{"line": 0, "character": 16},
					"end":   map[string]any{"line": 0, "character": 26},
				},
				"children": []any{},
			}}, nil)
		case protocol.MethodTextDocumentReferences:
			recorder.add(req.Method())
			referenceCalls.Add(1)

			var params protocol.ReferenceParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return err
			}
			assert.Equal(t, expectedURI, params.TextDocument.URI)

			return reply(ctx, []map[string]any{}, nil)
		default:
			return jsonrpc2.MethodNotFoundHandler(ctx, reply, req)
		}
	})
	clientConn.Go(t.Context(), jsonrpc2.MethodNotFoundHandler)

	t.Cleanup(func() {
		require.NoError(t, clientConn.Close())
		require.NoError(t, serverConn.Close())
		waitForConnDone(t, "clientConn", clientConn.Done())
		waitForConnDone(t, "serverConn", serverConn.Done())
	})

	return clientConn, recorder, &documentSymbolCalls, &referenceCalls, &openCalls, &closeCalls
}

// testReferenceFallbackConn builds an in-memory JSON-RPC peer that returns one raw reference in a file with no symbols.
func testReferenceFallbackConn(t *testing.T, targetURI uri.URI, referenceURI uri.URI) jsonrpc2.Conn {
	t.Helper()

	serverSide, clientSide := net.Pipe()
	serverConn := jsonrpc2.NewConn(jsonrpc2.NewStream(serverSide))
	clientConn := jsonrpc2.NewConn(jsonrpc2.NewStream(clientSide))

	serverConn.Go(t.Context(), func(ctx context.Context, reply jsonrpc2.Replier, req jsonrpc2.Request) error {
		switch req.Method() {
		case protocol.MethodTextDocumentDocumentSymbol:
			var params protocol.DocumentSymbolParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return err
			}

			switch filepath.Base(params.TextDocument.URI.Filename()) {
			case filepath.Base(targetURI.Filename()):
				return reply(ctx, []map[string]any{{
					"name": "makeBucket",
					"kind": int(protocol.SymbolKindFunction),
					"range": map[string]any{
						"start": map[string]any{"line": 0, "character": 0},
						"end":   map[string]any{"line": 0, "character": 30},
					},
					"selectionRange": map[string]any{
						"start": map[string]any{"line": 0, "character": 16},
						"end":   map[string]any{"line": 0, "character": 26},
					},
					"children": []any{},
				}}, nil)
			case filepath.Base(referenceURI.Filename()):
				return reply(ctx, []map[string]any{}, nil)
			default:
				return jsonrpc2.MethodNotFoundHandler(ctx, reply, req)
			}
		case protocol.MethodTextDocumentReferences:
			var params protocol.ReferenceParams
			err := json.Unmarshal(req.Params(), &params)
			if err != nil {
				t.Error(err)
				return err
			}
			assert.Equal(t, filepath.Base(targetURI.Filename()), filepath.Base(params.TextDocument.URI.Filename()))

			return reply(ctx, []map[string]any{{
				"uri": string(referenceURI),
				"range": map[string]any{
					"start": map[string]any{"line": 0, "character": 9},
					"end":   map[string]any{"line": 0, "character": 19},
				},
			}}, nil)
		default:
			return jsonrpc2.MethodNotFoundHandler(ctx, reply, req)
		}
	})
	clientConn.Go(t.Context(), jsonrpc2.MethodNotFoundHandler)

	t.Cleanup(func() {
		require.NoError(t, clientConn.Close())
		require.NoError(t, serverConn.Close())
		waitForConnDone(t, "clientConn", clientConn.Done())
		waitForConnDone(t, "serverConn", serverConn.Done())
	})

	return clientConn
}

// workflowMethodRecorder keeps request order assertions deterministic across the handler goroutine and the test goroutine.
type workflowMethodRecorder struct {
	mu      sync.Mutex
	records []string
}

// add appends one method name in arrival order.
func (r *workflowMethodRecorder) add(method string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.records = append(r.records, method)
}

// methods returns a stable snapshot of the recorded request order.
func (r *workflowMethodRecorder) methods() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.records...)
}

// waitForWorkflowMethods waits until the recorder sees the expected workflow order.
func waitForWorkflowMethods(t *testing.T, recorder *workflowMethodRecorder, expected []string) {
	t.Helper()

	require.Eventually(t, func() bool {
		return slices.Equal(expected, recorder.methods())
	}, time.Second, 10*time.Millisecond)
}

// waitForConnDone keeps shutdown regressions local to the exact cleanup wait site.
func waitForConnDone(t *testing.T, name string, done <-chan struct{}) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s shutdown", name)
	}
}

// memorySymbolTreeCache keeps stdlsp cache tests focused on behavior rather than filesystem details.
type memorySymbolTreeCache struct {
	mu         sync.Mutex
	entries    map[string][]byte
	readCount  int
	writeCount int
}

// ReadSymbolTree returns one stored payload when the request key matches an existing entry.
func (c *memorySymbolTreeCache) ReadSymbolTree(
	_ context.Context,
	request *ReadSymbolTreeCacheRequest,
) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.readCount++
	payload, ok := c.entries[memoryCacheKey(request.WorkspaceRoot, request.RelativePath, request.Metadata)]
	if !ok {
		return nil, false, nil
	}

	return append([]byte(nil), payload...), true, nil
}

// WriteSymbolTree stores one payload under the request key so later reads can reuse it.
func (c *memorySymbolTreeCache) WriteSymbolTree(
	_ context.Context,
	request *WriteSymbolTreeCacheRequest,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.writeCount++
	c.entries[memoryCacheKey(request.WorkspaceRoot, request.RelativePath, request.Metadata)] = append([]byte(nil), request.Payload...)

	return nil
}

// fixedCacheMetadata keeps cache-reuse tests focused on shared stdlsp behavior instead of adapter-specific metadata.
func fixedCacheMetadata(_ context.Context, _, _ string) (*SymbolTreeCacheMetadata, error) {
	return &SymbolTreeCacheMetadata{
		Enabled:                true,
		DisabledReason:         "",
		AdapterID:              "test-adapter",
		ProfileID:              "std",
		AdapterFingerprint:     "fingerprint-v1",
		AdditionalDependencies: nil,
	}, nil
}

func memoryCacheKey(workspaceRoot, relativePath string, metadata SymbolTreeCacheMetadata) string {
	return strings.Join([]string{workspaceRoot, relativePath, metadata.AdapterID, metadata.ProfileID, metadata.AdapterFingerprint}, "|")
}
