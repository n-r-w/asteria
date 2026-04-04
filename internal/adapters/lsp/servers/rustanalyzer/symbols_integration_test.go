//go:build integration_tests

package lsprustanalyzer

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
)

const rustFixtureDirPermissions = fs.FileMode(0o750)

// TestIntegrationServiceGetSymbolsOverviewReturnsFixtureSymbols proves that the live rust-analyzer-backed
// overview exposes stable top-level and nested Rust symbols from the fixture workspace.
func TestIntegrationServiceGetSymbolsOverviewReturnsFixtureSymbols(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     filepath.ToSlash(filepath.Join("src", "lib.rs")),
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindStruct), "Bucket")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindMethod), "Bucket/describe")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindFunction), "make_bucket")
}

// TestIntegrationServiceFindSymbolReturnsCanonicalStructMethod proves that canonical Rust impl-member paths
// still resolve after the adapter retries the raw rust-analyzer impl-container path and re-exports the match
// through the canonical path shape.
func TestIntegrationServiceFindSymbolReturnsCanonicalStructMethod(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "Bucket/describe"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            filepath.ToSlash(filepath.Join("src", "lib.rs")),
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "Bucket/describe")
	require.True(t, ok, "expected method match, got %#v", result.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("src", "lib.rs")), symbol.File)
}

// TestIntegrationServiceFindSymbolReturnsCanonicalNestedStructMethod proves that canonical nested Rust
// impl-member paths resolve against the live workspace, not only through the unit-level raw-path rewrite.
func TestIntegrationServiceFindSymbolReturnsCanonicalNestedStructMethod(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "nested/NestedBucket/describe"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            filepath.ToSlash(filepath.Join("src", "lib.rs")),
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "nested/NestedBucket/describe")
	require.True(t, ok, "expected nested method match, got %#v", result.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("src", "lib.rs")), symbol.File)
}

// TestIntegrationServiceGetSymbolsOverviewReturnsExpandedRustFixtureSymbols proves that the live overview
// exposes the broader Rust construct surface added to the existing fixture root, including nested modules,
// traits, enum members, tuple and unit structs, generics, visibility variants, and top-level values.
func TestIntegrationServiceGetSymbolsOverviewReturnsExpandedRustFixtureSymbols(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 3},
		WorkspaceRoot:            workspaceRoot,
		File:                     filepath.ToSlash(filepath.Join("src", "lib.rs")),
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	for _, testCase := range []struct {
		path string
		kind protocol.SymbolKind
	}{
		{path: "FIXTURE_STAMP", kind: protocol.SymbolKindConstant},
		{path: "FIXTURE_COUNTER", kind: protocol.SymbolKindConstant},
		{path: "bucket_in_lib", kind: protocol.SymbolKindFunction},
		{path: "nested/NestedBucket", kind: protocol.SymbolKindStruct},
		{path: "advanced/DisplayLabel", kind: protocol.SymbolKindInterface},
		{path: "advanced/DisplayLabel/render", kind: protocol.SymbolKindMethod},
		{path: "advanced/DisplayLabel for TupleBucket/render", kind: protocol.SymbolKindMethod},
		{path: "advanced/BucketState", kind: protocol.SymbolKindEnum},
		{path: "advanced/BucketState/Ready", kind: protocol.SymbolKindEnumMember},
		{path: "advanced/TupleBucket", kind: protocol.SymbolKindStruct},
		{path: "advanced/UnitBucket", kind: protocol.SymbolKindStruct},
		{path: "advanced/BucketAlias", kind: protocol.SymbolKindTypeParameter},
		{path: "advanced/GenericBucket", kind: protocol.SymbolKindStruct},
		{path: "advanced/CrateVisibleBucket", kind: protocol.SymbolKindStruct},
	} {
		assertKindContainsExactPath(t, result.Symbols, int(testCase.kind), testCase.path)
	}
}

// TestIntegrationServiceFindSymbolReturnsExpandedRustConstructs proves that canonical path lookup resolves
// the added Rust construct set and returns declaration bodies that still expose the syntax details the
// adapter makes observable.
func TestIntegrationServiceFindSymbolReturnsExpandedRustConstructs(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	testCases := []struct {
		name            string
		path            string
		expectedKind    protocol.SymbolKind
		requiredContent string
	}{
		{name: "top-level function", path: "bucket_in_lib", expectedKind: protocol.SymbolKindFunction, requiredContent: "make_bucket(\"primary\")"},
		{name: "nested struct", path: "nested/NestedBucket", expectedKind: protocol.SymbolKindStruct, requiredContent: "pub struct NestedBucket"},
		{name: "trait method", path: "advanced/DisplayLabel/render", expectedKind: protocol.SymbolKindMethod, requiredContent: "fn render(&self) -> String;"},
		{name: "trait impl method", path: "advanced/DisplayLabel for TupleBucket/render", expectedKind: protocol.SymbolKindMethod, requiredContent: "self.0.clone()"},
		{name: "enum variant", path: "advanced/BucketState/Ready", expectedKind: protocol.SymbolKindEnumMember},
		{name: "type alias", path: "advanced/BucketAlias", expectedKind: protocol.SymbolKindTypeParameter, requiredContent: "pub type BucketAlias = TupleBucket;"},
		{name: "generic struct", path: "advanced/GenericBucket", expectedKind: protocol.SymbolKindStruct, requiredContent: "pub struct GenericBucket<'a, const N: usize, T>"},
		{name: "async closure function", path: "advanced/load_label", expectedKind: protocol.SymbolKindFunction, requiredContent: "let prefix = || GLOBAL_LABEL.to_string();"},
		{name: "pattern binding function", path: "advanced/pattern_label", expectedKind: protocol.SymbolKindFunction, requiredContent: "let (head, tail) = input;"},
		{name: "crate-visible struct", path: "advanced/CrateVisibleBucket", expectedKind: protocol.SymbolKindStruct, requiredContent: "pub(crate) struct CrateVisibleBucket"},
		{name: "top-level const", path: "FIXTURE_STAMP", expectedKind: protocol.SymbolKindConstant, requiredContent: "pub const FIXTURE_STAMP"},
		{name: "top-level static", path: "FIXTURE_COUNTER", expectedKind: protocol.SymbolKindConstant, requiredContent: "pub static FIXTURE_COUNTER"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
				FindSymbolFilter: domain.FindSymbolFilter{Path: testCase.path, IncludeBody: true},
				WorkspaceRoot:    workspaceRoot,
				Scope:            filepath.ToSlash(filepath.Join("src", "lib.rs")),
			})
			require.NoError(t, err)

			assertFoundSymbolMatches(
				t,
				result.Symbols,
				testCase.path,
				int(testCase.expectedKind),
				filepath.ToSlash(filepath.Join("src", "lib.rs")),
				testCase.requiredContent,
			)
		})
	}
}

// TestIntegrationServiceExposesExportedMacro proves that the shared documentSymbol-based declaration surface
// already exposes supported `#[macro_export] macro_rules!` items through overview and find-symbol queries.
func TestIntegrationServiceExposesExportedMacro(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	overview, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     filepath.ToSlash(filepath.Join("src", "lib.rs")),
	})
	require.NoError(t, err)

	location, ok := helpers.FindOverviewSymbol(overview.Symbols, "exported_bucket_macro")
	require.Truef(t, ok, "expected exported macro in overview, got %#v", overview.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("src", "lib.rs")), location.File)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "exported_bucket_macro", IncludeBody: true},
		WorkspaceRoot:    workspaceRoot,
		Scope:            filepath.ToSlash(filepath.Join("src", "lib.rs")),
	})
	require.NoError(t, err)

	assertFoundSymbolMatches(
		t,
		result.Symbols,
		"exported_bucket_macro",
		int(location.Kind),
		filepath.ToSlash(filepath.Join("src", "lib.rs")),
		"macro_rules! exported_bucket_macro",
	)
}

// TestIntegrationServiceGetSymbolsOverviewReturnsSecondaryCrateSymbols proves that the secondary crate is
// asserted as a real symbol surface, not only as a runtime-isolation fixture.
func TestIntegrationServiceGetSymbolsOverviewReturnsSecondaryCrateSymbols(t *testing.T) {
	workspaceRoot := rustSecondaryFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     filepath.ToSlash(filepath.Join("src", "lib.rs")),
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	for _, testCase := range []struct {
		path string
		kind protocol.SymbolKind
	}{
		{path: "SecondaryBucket", kind: protocol.SymbolKindStruct},
		{path: "SecondaryBucket/describe", kind: protocol.SymbolKindMethod},
		{path: "make_secondary_bucket", kind: protocol.SymbolKindFunction},
	} {
		assertKindContainsExactPath(t, result.Symbols, int(testCase.kind), testCase.path)
	}
}

// TestIntegrationServiceFindSymbolReturnsSecondaryCrateSymbols proves that canonical lookup also resolves
// symbol paths from the second crate fixture root.
func TestIntegrationServiceFindSymbolReturnsSecondaryCrateSymbols(t *testing.T) {
	workspaceRoot := rustSecondaryFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	testCases := []struct {
		name         string
		path         string
		expectedKind protocol.SymbolKind
	}{
		{name: "struct", path: "SecondaryBucket", expectedKind: protocol.SymbolKindStruct},
		{name: "method", path: "SecondaryBucket/describe", expectedKind: protocol.SymbolKindMethod},
		{name: "function", path: "make_secondary_bucket", expectedKind: protocol.SymbolKindFunction},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
				FindSymbolFilter: domain.FindSymbolFilter{Path: testCase.path},
				WorkspaceRoot:    workspaceRoot,
				Scope:            filepath.ToSlash(filepath.Join("src", "lib.rs")),
			})
			require.NoError(t, err)

			assertFoundSymbolMatches(
				t,
				result.Symbols,
				testCase.path,
				int(testCase.expectedKind),
				filepath.ToSlash(filepath.Join("src", "lib.rs")),
				"",
			)
		})
	}
}

// TestIntegrationServiceFindReferencingSymbolsGroupsReferences proves that live reference lookup groups Rust
// usages by the smallest containing symbol exposed in the referencing file.
func TestIntegrationServiceFindReferencingSymbolsGroupsReferences(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "make_bucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         filepath.ToSlash(filepath.Join("src", "lib.rs")),
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "use_bucket")
	require.True(t, ok, "expected grouped references, got %#v", result.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("src", "references.rs")), symbol.File)
	assert.Contains(t, symbol.Content, "make_bucket(\"secondary\")")
}

// TestIntegrationServiceFindReferencingSymbolsGroupsSameFileReferences proves that live grouped-reference
// lookup keeps same-file Rust references visible instead of dropping them when the target and caller share a file.
func TestIntegrationServiceFindReferencingSymbolsGroupsSameFileReferences(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "bucket_in_lib"},
		WorkspaceRoot:                workspaceRoot,
		File:                         filepath.ToSlash(filepath.Join("src", "lib.rs")),
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "use_bucket_in_same_file")
	require.True(t, ok, "expected same-file grouped references, got %#v", result.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("src", "lib.rs")), symbol.File)
	assert.Contains(t, symbol.Content, "bucket_in_lib()")
}

// TestIntegrationServiceFindReferencingSymbolsAcceptsCanonicalMethodPath proves that callers can reuse the
// canonical method path returned by overview/find_symbol as-is when they switch to find_referencing_symbols.
func TestIntegrationServiceFindReferencingSymbolsAcceptsCanonicalMethodPath(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	findSymbolResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "Bucket/describe"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            filepath.ToSlash(filepath.Join("src", "lib.rs")),
	})
	require.NoError(t, err)

	targetSymbol, ok := findFoundSymbol(findSymbolResult.Symbols, "Bucket/describe")
	require.True(t, ok, "expected canonical method match, got %#v", findSymbolResult.Symbols)

	referencesResult, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: targetSymbol.Path},
		WorkspaceRoot:                workspaceRoot,
		File:                         targetSymbol.File,
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(referencesResult.Symbols, "use_bucket")
	require.True(t, ok, "expected grouped references for canonical method path, got %#v", referencesResult.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("src", "references.rs")), symbol.File)
	assert.Contains(t, symbol.Content, "bucket.describe()")
}

// TestIntegrationServiceConcurrentSameRootReusesSingleConnection proves that concurrent callers for one
// workspace root share the same live runtime connection.
func TestIntegrationServiceConcurrentSameRootReusesSingleConnection(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
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
	firstResult := <-results
	secondResult := <-results
	require.NoError(t, firstResult.err)
	require.NoError(t, secondResult.err)
	assert.Same(t, firstResult.conn, secondResult.conn)
}

// TestIntegrationServiceConcurrentDifferentRootsKeepSeparateConnections proves that different workspace roots
// keep separate runtime sessions even when startup happens concurrently.
func TestIntegrationServiceConcurrentDifferentRootsKeepSeparateConnections(t *testing.T) {
	firstRoot := rustFixtureRoot(t)
	secondRoot := rustSecondaryFixtureRoot(t)
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
	firstResult := <-results
	secondResult := <-results
	require.NoError(t, firstResult.err)
	require.NoError(t, secondResult.err)
	assert.NotSame(t, firstResult.conn, secondResult.conn)
}

// TestIntegrationServiceConcurrentInvalidRootDoesNotPoisonValidRoot proves that one bad workspace root does
// not break a concurrent successful request on a different root.
func TestIntegrationServiceConcurrentInvalidRootDoesNotPoisonValidRoot(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	type overviewResult struct {
		result domain.GetSymbolsOverviewResult
		err    error
	}

	start := make(chan struct{})
	validResults := make(chan overviewResult, 1)
	invalidResults := make(chan error, 1)
	var waitGroup sync.WaitGroup

	waitGroup.Go(func() {
		<-start
		result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
			WorkspaceRoot:            workspaceRoot,
			File:                     filepath.ToSlash(filepath.Join("src", "lib.rs")),
		})
		validResults <- overviewResult{result: result, err: err}
	})

	waitGroup.Go(func() {
		<-start
		_, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
			WorkspaceRoot:            filepath.Join(workspaceRoot, "missing-root"),
			File:                     filepath.ToSlash(filepath.Join("src", "lib.rs")),
		})
		invalidResults <- err
	})

	close(start)
	waitGroup.Wait()

	valid := <-validResults
	require.NoError(t, valid.err)
	assert.NotEmpty(t, valid.result.Symbols)

	invalidErr := <-invalidResults
	require.Error(t, invalidErr)
}

// assertKindContainsExactPath keeps the integration test readable when a symbol path should match exactly.
func assertKindContainsExactPath(t *testing.T, symbols []domain.SymbolLocation, kind int, path string) {
	t.Helper()

	location, ok := helpers.FindOverviewSymbol(symbols, path)
	require.Truef(t, ok, "expected %q in overview, got: %#v", path, symbols)
	assert.Equal(t, kind, location.Kind)
	assert.Equal(t, filepath.ToSlash(filepath.Join("src", "lib.rs")), location.File)
	assert.GreaterOrEqual(t, location.StartLine, 0)
	assert.GreaterOrEqual(t, location.EndLine, location.StartLine)
}

// assertFoundSymbolMatches keeps find-symbol assertions short when the test needs a stable kind and file match,
// plus an optional body snippet that proves the returned declaration still exposes the expected syntax.
func assertFoundSymbolMatches(
	t *testing.T,
	symbols []domain.FoundSymbol,
	namePath string,
	expectedKind int,
	expectedFile string,
	requiredContent string,
) {
	t.Helper()

	symbol, ok := findFoundSymbol(symbols, namePath)
	require.Truef(t, ok, "expected %q in find_symbol results, got %#v", namePath, symbols)
	assert.Equal(t, expectedKind, symbol.Kind)
	assert.Equal(t, expectedFile, symbol.File)
	if requiredContent != "" {
		assert.Contains(t, symbol.Body, requiredContent)
	}
}

// newIntegrationService keeps repeated rust-analyzer service setup out of individual integration scenarios.
func newIntegrationService(t *testing.T) (*Service, context.Context) {
	t.Helper()

	service, err := New(t.TempDir(), cfgadapters.RustAnalyzerConfig{
		WorkspaceSymbolSearchLimit: 128,
		StartupReadyTimeout:        30 * time.Second,
	})
	require.NoError(t, err)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, service.Close(ctx))
	})

	return service, ctx
}

// rustFixtureRoot returns the absolute path to the stable Rust fixture project used by integration tests.
func rustFixtureRoot(t *testing.T) string {
	t.Helper()

	requireRustAnalyzerInstalled(t)

	return copyRustFixtureRoot(t, "basic")
}

// rustSecondaryFixtureRoot returns the absolute path to the secondary Rust fixture project used by multi-root tests.
func rustSecondaryFixtureRoot(t *testing.T) string {
	t.Helper()

	requireRustAnalyzerInstalled(t)

	return copyRustFixtureRoot(t, "secondary")
}

// copyRustFixtureRoot copies the tracked fixture tree into a temp workspace so live rust-analyzer runs never
// write state back into the repository tree.
func copyRustFixtureRoot(t *testing.T, fixtureName string) string {
	t.Helper()

	trackedRoot := trackedRustFixtureRoot(t, fixtureName)
	workspaceRoot := filepath.Join(t.TempDir(), fixtureName)
	require.NotEqual(t, trackedRoot, workspaceRoot)
	require.NoError(t, copyRustFixtureTree(trackedRoot, workspaceRoot))

	return workspaceRoot
}

// copyRustFixtureTree recreates the tracked fixture files under a temp workspace while skipping generated build
// output that would make integration validation depend on a developer's checkout.
func copyRustFixtureTree(sourceRoot string, destinationRoot string) error {
	return filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relativePath, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}

		if relativePath == "." {
			return os.MkdirAll(destinationRoot, rustFixtureDirPermissions)
		}

		if shouldSkipRustFixtureEntry(relativePath) {
			if entry.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}

		destinationPath := filepath.Join(destinationRoot, relativePath)
		if entry.IsDir() {
			return os.MkdirAll(destinationPath, entryInfo.Mode().Perm())
		}

		fileContent, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(destinationPath, fileContent, entryInfo.Mode().Perm())
	})
}

// shouldSkipRustFixtureEntry keeps generated Cargo build output out of the temp workspace copy so tests prove
// behavior without depending on checkout residue.
func shouldSkipRustFixtureEntry(relativePath string) bool {
	switch filepath.Base(relativePath) {
	case rustTargetDirName:
		return true
	default:
		return false
	}
}

// trackedRustFixtureRoot returns the repository fixture root so integration tests can prove they are using
// isolated temp workspaces instead of mutating source-controlled directories.
func trackedRustFixtureRoot(t *testing.T, fixtureName string) string {
	t.Helper()

	fixtureRoot, err := filepath.Abs(filepath.Join("testdata", fixtureName))
	require.NoError(t, err)

	return fixtureRoot
}

// requireRustAnalyzerInstalled fails fast with a clear message when the external rust-analyzer prerequisite is missing.
func requireRustAnalyzerInstalled(t *testing.T) {
	t.Helper()

	_, err := exec.LookPath(rustAnalyzerServerName)
	require.NoError(t, err, "rust-analyzer must be installed and available in PATH")
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

// findReferencingSymbol keeps grouped-reference assertions short and focused on behavior.
func findReferencingSymbol(symbols []domain.ReferencingSymbol, namePath string) (domain.ReferencingSymbol, bool) {
	for _, symbol := range symbols {
		if symbol.Path == namePath {
			return symbol, true
		}
	}

	return domain.ReferencingSymbol{}, false
}
