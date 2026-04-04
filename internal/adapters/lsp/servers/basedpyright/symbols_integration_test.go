//go:build integration_tests

package lspbasedpyright

import (
	"context"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
)

// TestIntegrationServiceGetSymbolsOverviewReturnsFixtureSymbols proves that the live basedpyright-backed
// overview exposes stable top-level and nested Python symbols from the fixture workspace.
func TestIntegrationServiceGetSymbolsOverviewReturnsFixtureSymbols(t *testing.T) {
	workspaceRoot := basedpyrightFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.py",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindConstant), "FIXTURE_STAMP")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindVariable), "fixture_counter")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindClass), "Bucket")
	assertOverviewContainsExactPathInFile(t, result.Symbols, "fixture.py", "Bucket/__init__")
	assertOverviewContainsExactPathInFile(t, result.Symbols, "fixture.py", "Bucket/value")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindMethod), "Bucket/describe")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindFunction), "make_bucket")
}

// TestIntegrationServiceFindSymbolReturnsMethodBody proves that canonical Python class-member paths resolve
// through stdlsp and can return the source body.
func TestIntegrationServiceFindSymbolReturnsMethodBody(t *testing.T) {
	workspaceRoot := basedpyrightFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "Bucket/describe",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "fixture.py",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "Bucket/describe")
	require.True(t, ok, "expected method match, got %#v", result.Symbols)
	assert.Equal(t, "fixture.py", symbol.File)
	assert.Contains(t, symbol.Body, "def describe(self) -> str")
}

// TestIntegrationServiceFindSymbolIncludeInfoReturnsHover proves that hover-backed include_info works for Python symbols.
func TestIntegrationServiceFindSymbolIncludeInfoReturnsHover(t *testing.T) {
	workspaceRoot := basedpyrightFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "make_bucket",
			IncludeInfo: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "fixture.py",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "make_bucket")
	require.True(t, ok, "expected function match, got %#v", result.Symbols)
	assert.Equal(t, "fixture.py", symbol.File)
	assert.NotEmpty(t, symbol.Info)
	assert.Contains(t, symbol.Info, "make_bucket")
}

// TestIntegrationServiceFindSymbolReturnsFieldAndLocalBindings proves that basedpyright exposes exact
// name paths for instance fields, parameters, and locals that affect Python scope resolution.
func TestIntegrationServiceFindSymbolReturnsFieldAndLocalBindings(t *testing.T) {
	workspaceRoot := basedpyrightFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	testCases := []struct {
		name  string
		path  string
		scope string
		file  string
	}{
		{name: "constructor", path: "Bucket/__init__", scope: "fixture.py", file: "fixture.py"},
		{name: "instance field", path: "Bucket/value", scope: "fixture.py", file: "fixture.py"},
		{name: "function parameter", path: "make_bucket/value", scope: "fixture.py", file: "fixture.py"},
		{name: "local left binding", path: "use_bucket/left", scope: "references.py", file: "references.py"},
		{name: "local right binding", path: "use_bucket/right", scope: "references.py", file: "references.py"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
				FindSymbolFilter: domain.FindSymbolFilter{Path: testCase.path},
				WorkspaceRoot:    workspaceRoot,
				Scope:            testCase.scope,
			})
			require.NoError(t, err)

			requireFoundSymbolInFile(t, result.Symbols, testCase.path, testCase.file)
		})
	}
}

// TestIntegrationServiceFindReferencingSymbolsGroupsReferences proves that live reference lookup groups Python
// usages by the smallest containing symbol exposed in the referencing file.
func TestIntegrationServiceFindReferencingSymbolsGroupsReferences(t *testing.T) {
	workspaceRoot := basedpyrightFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "make_bucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.py",
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "use_bucket")
	require.True(t, ok, "expected grouped references, got %#v", result.Symbols)
	assert.Equal(t, "references.py", symbol.File)
	assert.Contains(t, symbol.Content, "make_bucket(\"secondary\")")
}

// TestIntegrationServiceFindSymbolSupportsWholeWorkspaceScope proves that an empty scope searches the full
// selected Python workspace and still resolves symbols from non-declaration files.
func TestIntegrationServiceFindSymbolSupportsWholeWorkspaceScope(t *testing.T) {
	workspaceRoot := basedpyrightFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "use_bucket"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "use_bucket")
	require.True(t, ok, "expected whole-workspace match, got %#v", result.Symbols)
	assert.Equal(t, "references.py", symbol.File)
}

// TestIntegrationServiceGetSymbolsOverviewIncludesAdvancedPythonForms proves that the live adapter keeps
// advanced Python declarations observable without needing shared-layer changes.
func TestIntegrationServiceGetSymbolsOverviewIncludesAdvancedPythonForms(t *testing.T) {
	workspaceRoot := basedpyrightFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	advancedResult, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 2},
		WorkspaceRoot:            workspaceRoot,
		File:                     "advanced.py",
	})
	require.NoError(t, err)
	require.NotEmpty(t, advancedResult.Symbols)

	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "ValueT")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "BucketPair")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "DecoratedBucket")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "DecoratedBucket/label")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "DecoratedBucket/from_parts")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "DecoratedBucket/build_default")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "DerivedBucket")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "DerivedBucket/render")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "choose_bucket")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "build_labeler")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "build_labeler/apply")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "collect_labels")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "drain_labels")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "iter_labels")
	assertOverviewContainsExactPathInFile(t, advancedResult.Symbols, "advanced.py", "sync_labels")

	modelsResult, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "pkg/models.py",
	})
	require.NoError(t, err)
	require.NotEmpty(t, modelsResult.Symbols)
	assertOverviewContainsExactPathInFile(t, modelsResult.Symbols, "pkg/models.py", "make_imported_bucket")

	consumerResult, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "pkg/consumer.py",
	})
	require.NoError(t, err)
	require.NotEmpty(t, consumerResult.Symbols)
	assertOverviewContainsExactPathInFile(t, consumerResult.Symbols, "pkg/consumer.py", "consume_relative_bucket")
}

// TestIntegrationServiceFindSymbolReturnsAdvancedPythonBodies proves that advanced Python symbols keep
// exact paths and source bodies across decorators, closures, async functions, and generators.
func TestIntegrationServiceFindSymbolReturnsAdvancedPythonBodies(t *testing.T) {
	workspaceRoot := basedpyrightFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	testCases := []struct {
		name    string
		path    string
		scope   string
		file    string
		snippet string
	}{
		{name: "property", path: "DecoratedBucket/label", scope: "advanced.py", file: "advanced.py", snippet: "@property"},
		{name: "classmethod", path: "DecoratedBucket/from_parts", scope: "advanced.py", file: "advanced.py", snippet: "@classmethod"},
		{name: "staticmethod", path: "DecoratedBucket/build_default", scope: "advanced.py", file: "advanced.py", snippet: "@staticmethod"},
		{name: "derived render", path: "DerivedBucket/render", scope: "advanced.py", file: "advanced.py", snippet: "return self.label.upper()"},
		{name: "nested closure", path: "build_labeler/apply", scope: "advanced.py", file: "advanced.py", snippet: "return f\"{prefix}:{value}\""},
		{name: "match case", path: "choose_bucket", scope: "advanced.py", file: "advanced.py", snippet: "match value:"},
		{name: "comprehension", path: "collect_labels", scope: "advanced.py", file: "advanced.py", snippet: "return [labeler(value) for value in values]"},
		{name: "async function", path: "drain_labels", scope: "advanced.py", file: "advanced.py", snippet: "async for value in iter_labels(values)"},
		{name: "generator", path: "sync_labels", scope: "advanced.py", file: "advanced.py", snippet: "yield value"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
				FindSymbolFilter: domain.FindSymbolFilter{
					Path:        testCase.path,
					IncludeBody: true,
				},
				WorkspaceRoot: workspaceRoot,
				Scope:         testCase.scope,
			})
			require.NoError(t, err)

			symbol := requireFoundSymbolInFile(t, result.Symbols, testCase.path, testCase.file)
			assert.Contains(t, symbol.Body, testCase.snippet)
		})
	}
}

// TestIntegrationServiceFindReferencingSymbolsGroupsMethodAndRelativeImportReferences proves that grouped
// Python references remain stable for member calls and relative-import call sites.
func TestIntegrationServiceFindReferencingSymbolsGroupsMethodAndRelativeImportReferences(t *testing.T) {
	workspaceRoot := basedpyrightFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	testCases := []struct {
		name            string
		file            string
		path            string
		expectedPath    string
		expectedFile    string
		expectedContent []string
	}{
		{
			name:            "method calls in basic references",
			file:            "fixture.py",
			path:            "Bucket/describe",
			expectedPath:    "use_bucket",
			expectedFile:    "references.py",
			expectedContent: []string{"left.describe()", "right.describe()"},
		},
		{
			name:            "classmethod call in match branch",
			file:            "advanced.py",
			path:            "DecoratedBucket/from_parts",
			expectedPath:    "choose_bucket",
			expectedFile:    "advanced.py",
			expectedContent: []string{"DecoratedBucket.from_parts((\"left\", \"right\"))"},
		},
		{
			name:            "staticmethod call in fallback branch",
			file:            "advanced.py",
			path:            "DecoratedBucket/build_default",
			expectedPath:    "choose_bucket",
			expectedFile:    "advanced.py",
			expectedContent: []string{"DecoratedBucket.build_default()"},
		},
		{
			name:            "relative import call",
			file:            "pkg/models.py",
			path:            "make_imported_bucket",
			expectedPath:    "consume_relative_bucket",
			expectedFile:    "pkg/consumer.py",
			expectedContent: []string{"make_imported_bucket(\"pkg\")"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
				FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: testCase.path},
				WorkspaceRoot:                workspaceRoot,
				File:                         testCase.file,
			})
			require.NoError(t, err)

			symbol, ok := findReferencingSymbol(result.Symbols, testCase.expectedPath)
			require.True(t, ok, "expected grouped references for %q, got %#v", testCase.path, result.Symbols)
			assert.Equal(t, testCase.expectedFile, symbol.File)
			for _, snippet := range testCase.expectedContent {
				assert.Contains(t, symbol.Content, snippet)
			}
		})
	}
}

// TestIntegrationServiceSecondaryWorkspaceExposesWorkerSymbol proves that the second fixture root validates
// real symbol behavior instead of only runtime-session isolation.
func TestIntegrationServiceSecondaryWorkspaceExposesWorkerSymbol(t *testing.T) {
	workspaceRoot := basedpyrightSecondaryFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	overview, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "worker.py",
	})
	require.NoError(t, err)
	require.NotEmpty(t, overview.Symbols)
	assertOverviewContainsExactPathInFile(t, overview.Symbols, "worker.py", "run_worker")

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "run_worker",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "worker.py",
	})
	require.NoError(t, err)

	symbol := requireFoundSymbolInFile(t, result.Symbols, "run_worker", "worker.py")
	assert.Contains(t, symbol.Body, "return value.upper()")
}

// TestIntegrationServiceConcurrentSameRootReusesSingleConnection proves that concurrent callers for one
// workspace root share the same live runtime connection.
func TestIntegrationServiceConcurrentSameRootReusesSingleConnection(t *testing.T) {
	workspaceRoot := basedpyrightFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	type ensureConnResult struct {
		conn any
		err  error
	}

	start := make(chan struct{})
	results := make(chan ensureConnResult, 2)
	var waitGroup sync.WaitGroup
	for range 2 {
		waitGroup.Go(func() {
			<-start
			conn, err := service.rt.EnsureConn(ctx, workspaceRoot)
			results <- ensureConnResult{conn: conn, err: err}
		})
	}

	close(start)
	waitGroup.Wait()
	close(results)

	collectedResults := make([]ensureConnResult, 0, 2)
	for result := range results {
		collectedResults = append(collectedResults, result)
	}
	require.Len(t, collectedResults, 2)
	require.NoError(t, collectedResults[0].err)
	require.NoError(t, collectedResults[1].err)
	assert.Same(t, collectedResults[0].conn, collectedResults[1].conn)
}

// TestIntegrationServiceConcurrentDifferentRootsKeepSeparateConnections proves that different workspace roots
// keep separate runtime sessions even when startup happens concurrently.
func TestIntegrationServiceConcurrentDifferentRootsKeepSeparateConnections(t *testing.T) {
	firstRoot := basedpyrightFixtureRoot(t)
	secondRoot := basedpyrightSecondaryFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	type ensureConnResult struct {
		conn any
		err  error
	}

	start := make(chan struct{})
	results := make(chan ensureConnResult, 2)
	var waitGroup sync.WaitGroup
	for _, workspaceRoot := range []string{firstRoot, secondRoot} {
		workspaceRoot := workspaceRoot
		waitGroup.Go(func() {
			<-start
			conn, err := service.rt.EnsureConn(ctx, workspaceRoot)
			results <- ensureConnResult{conn: conn, err: err}
		})
	}

	close(start)
	waitGroup.Wait()
	close(results)

	collectedResults := make([]ensureConnResult, 0, 2)
	for result := range results {
		collectedResults = append(collectedResults, result)
	}
	require.Len(t, collectedResults, 2)
	require.NoError(t, collectedResults[0].err)
	require.NoError(t, collectedResults[1].err)
	assert.NotSame(t, collectedResults[0].conn, collectedResults[1].conn)
}

// TestIntegrationServiceConcurrentInvalidRootDoesNotPoisonValidRoot proves that one bad workspace root
// does not break a concurrent successful request on a different root.
func TestIntegrationServiceConcurrentInvalidRootDoesNotPoisonValidRoot(t *testing.T) {
	workspaceRoot := basedpyrightFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	type overviewResult struct {
		result domain.GetSymbolsOverviewResult
		err    error
	}

	start := make(chan struct{})
	validResults := make(chan overviewResult, 1)
	invalidResults := make(chan error, 1)
	var waitGroup sync.WaitGroup

	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		<-start
		result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
			WorkspaceRoot:            workspaceRoot,
			File:                     "fixture.py",
		})
		validResults <- overviewResult{result: result, err: err}
	}()

	waitGroup.Go(func() {
		<-start
		_, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
			WorkspaceRoot:            filepath.Join(workspaceRoot, "missing-root"),
			File:                     "fixture.py",
		})
		invalidResults <- err
	})

	close(start)
	waitGroup.Wait()
	close(validResults)
	close(invalidResults)

	valid := <-validResults
	require.NoError(t, valid.err)
	assert.NotEmpty(t, valid.result.Symbols)

	invalidErr := <-invalidResults
	require.Error(t, invalidErr)
}

// assertKindContainsExactPath keeps the integration test readable when a symbol path should match exactly.
func assertKindContainsExactPath(t *testing.T, symbols []domain.SymbolLocation, kind int, path string) {
	t.Helper()

	location := assertOverviewContainsExactPathInFile(t, symbols, "fixture.py", path)
	assert.Equal(t, kind, location.Kind)
}

// assertOverviewContainsExactPathInFile keeps overview assertions focused on exact symbol paths regardless of file.
func assertOverviewContainsExactPathInFile(t *testing.T, symbols []domain.SymbolLocation, filePath, path string) domain.SymbolLocation {
	t.Helper()

	location, ok := helpers.FindOverviewSymbol(symbols, path)
	require.Truef(t, ok, "expected %q in overview, got: %#v", path, symbols)
	assert.Equal(t, filePath, location.File)
	assert.GreaterOrEqual(t, location.StartLine, 0)
	assert.GreaterOrEqual(t, location.EndLine, location.StartLine)

	return location
}

// newIntegrationService keeps repeated basedpyright service setup out of individual integration scenarios.
func newIntegrationService(t *testing.T) (*Service, context.Context) {
	t.Helper()

	service, err := New()
	require.NoError(t, err)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, service.Close(ctx))
	})

	return service, ctx
}

// basedpyrightFixtureRoot returns the absolute path to the stable Python fixture project used by integration tests.
func basedpyrightFixtureRoot(t *testing.T) string {
	t.Helper()

	requireBasedpyrightInstalled(t)

	fixtureRoot, err := filepath.Abs(filepath.Join("testdata", "basic"))
	require.NoError(t, err)

	return fixtureRoot
}

// basedpyrightSecondaryFixtureRoot returns the absolute path to the secondary Python fixture project used by multi-root tests.
func basedpyrightSecondaryFixtureRoot(t *testing.T) string {
	t.Helper()

	requireBasedpyrightInstalled(t)

	fixtureRoot, err := filepath.Abs(filepath.Join("testdata", "secondary"))
	require.NoError(t, err)

	return fixtureRoot
}

// requireBasedpyrightInstalled fails fast with a clear message when the external basedpyright prerequisite is missing.
func requireBasedpyrightInstalled(t *testing.T) {
	t.Helper()

	_, err := exec.LookPath(basedpyrightServerName)
	require.NoError(t, err, "basedpyright-langserver must be installed and available in PATH")
}

// findFoundSymbol keeps integration assertions readable when find_symbol returns a flat slice of matches.
func findFoundSymbol(symbols []domain.FoundSymbol, namePath string) (domain.FoundSymbol, bool) {
	for _, symbol := range symbols {
		if symbol.Path == namePath {
			return symbol, true
		}
	}

	return domain.FoundSymbol{}, false
}

// requireFoundSymbolInFile keeps exact-path find_symbol assertions short when multiple fixture files exist.
func requireFoundSymbolInFile(t *testing.T, symbols []domain.FoundSymbol, namePath, filePath string) domain.FoundSymbol {
	t.Helper()

	symbol, ok := findFoundSymbol(symbols, namePath)
	require.Truef(t, ok, "expected %q in find_symbol result, got %#v", namePath, symbols)
	assert.Equal(t, filePath, symbol.File)

	return symbol
}

// findReferencingSymbol keeps grouped-reference assertions short and focused on behavior.
func findReferencingSymbol(symbols []domain.ReferencingSymbol, namePath string) (domain.ReferencingSymbol, bool) {
	for _, symbol := range symbols {
		if symbol.Path == namePath {
			return symbol, true
		}
	}

	return domain.ReferencingSymbol{}, false
}
