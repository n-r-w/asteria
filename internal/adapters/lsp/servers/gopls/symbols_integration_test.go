//go:build integration_tests

package lspgopls

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

const (
	goplsFixtureDirPermissions  = 0o755
	goplsFixtureFilePermissions = 0o600
	goplsLiveWaitTimeout        = 10 * time.Second
	goplsLiveWaitTick           = 50 * time.Millisecond
)

// TestIntegrationServiceGetSymbolsOverviewReturnsFixtureSymbols proves that the live gopls-backed overview
// uses only the stable fixture module under testdata.
func TestIntegrationServiceGetSymbolsOverviewReturnsFixtureSymbols(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, err := New(cfgadapters.GoplsConfig{})
	require.NoError(t, err)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, service.Close(ctx))
	})

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{
			Depth: 1,
		},
		WorkspaceRoot: workspaceRoot,
		File:          "fixture.go",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)
	assert.ElementsMatch(t, []int{
		int(protocol.SymbolKindMethod),
		int(protocol.SymbolKindInterface),
		int(protocol.SymbolKindFunction),
		int(protocol.SymbolKindVariable),
		int(protocol.SymbolKindConstant),
		int(protocol.SymbolKindField),
		int(protocol.SymbolKindStruct),
	}, overviewKinds(result.Symbols))

	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindConstant), "FixtureStamp")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindVariable), "FixtureCounter")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindInterface), "FixtureContract")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindStruct), "FixtureBucket")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindFunction), "MakeBucket")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindField), "FixtureBucket/Label")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindField), "FixtureBucket/Value")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindMethod), "FixtureBucket/MeasureDepth")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindMethod), "FixtureBucket/Describe")
}

// TestIntegrationServiceGetSymbolsOverviewReturnsNestedFixtureSymbolsFromParentWorkspaceRoot proves that
// overview requests still publish canonical nested-module symbol paths when the gopls session root sits above the module.
func TestIntegrationServiceGetSymbolsOverviewReturnsNestedFixtureSymbolsFromParentWorkspaceRoot(t *testing.T) {
	workspaceRoot := goplsParentWorkspaceRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{
			Depth: 1,
		},
		WorkspaceRoot: workspaceRoot,
		File:          parentWorkspaceFixtureRelativePath("fixture.go"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	assertKindContainsExactPathInFile(
		t,
		result.Symbols,
		int(protocol.SymbolKindStruct),
		"FixtureBucket",
		parentWorkspaceFixtureRelativePath("fixture.go"),
	)
	assertKindContainsExactPathInFile(
		t,
		result.Symbols,
		int(protocol.SymbolKindField),
		"FixtureBucket/Label",
		parentWorkspaceFixtureRelativePath("fixture.go"),
	)
	assertKindContainsExactPathInFile(
		t,
		result.Symbols,
		int(protocol.SymbolKindMethod),
		"FixtureBucket/Describe",
		parentWorkspaceFixtureRelativePath("fixture.go"),
	)
}

// assertKindContainsExactPath keeps the integration test readable when a symbol path should match exactly.
func assertKindContainsExactPath(
	t *testing.T,
	symbols []domain.SymbolLocation,
	kind int,
	path string,
) {
	t.Helper()

	assertKindContainsExactPathInFile(t, symbols, kind, path, "fixture.go")
}

// assertKindContainsExactPathInFile keeps overview assertions reusable when integration scenarios use nested or cross-package files.
func assertKindContainsExactPathInFile(
	t *testing.T,
	symbols []domain.SymbolLocation,
	kind int,
	path string,
	file string,
) {
	t.Helper()

	location, ok := helpers.FindOverviewSymbol(symbols, path)
	require.Truef(t, ok, "expected %q in overview, got: %#v", path, symbols)
	assert.Equal(t, kind, location.Kind)
	assert.Equal(t, file, location.File)
	assert.GreaterOrEqual(t, location.StartLine, 0)
	assert.GreaterOrEqual(t, location.EndLine, location.StartLine)
}

// newIntegrationService keeps repeated gopls service setup out of individual integration scenarios.
func newIntegrationService(t *testing.T) (*Service, context.Context) {
	t.Helper()

	return newIntegrationServiceWithConfig(t, cfgadapters.GoplsConfig{})
}

// newIntegrationServiceWithConfig keeps adapter-configured gopls service setup out of individual integration scenarios.
func newIntegrationServiceWithConfig(t *testing.T, config cfgadapters.GoplsConfig) (*Service, context.Context) {
	t.Helper()

	service, err := New(config)
	require.NoError(t, err)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, service.Close(ctx))
	})

	return service, ctx
}

// newIntegrationStdService keeps raw stdlsp setup focused on the one behavior toggle under test.
func newIntegrationStdService(
	t *testing.T,
	service *Service,
	namePathBuilder stdlsp.NamePathBuilder,
) *stdlsp.Service {
	t.Helper()

	stdService, err := stdlsp.New(&stdlsp.Config{
		Extensions:                   extensions,
		EnsureConn:                   service.rt.EnsureConn,
		WithRequestDocument:          nil,
		OpenFileForDocumentSymbol:    false,
		OpenFileForReferenceWorkflow: false,
		BuildNamePath:                namePathBuilder,
		IgnoreDir:                    shouldIgnoreDir,
		SymbolTreeCache:              nil,
		BuildSymbolTreeCacheMetadata: nil,
	})
	require.NoError(t, err)

	return stdService
}

// TestIntegrationServiceFindSymbolNormalizesGoMethods proves that live find_symbol requests can
// find Go methods via canonical type/method paths instead of raw receiver prefixes.
func TestIntegrationServiceFindSymbolNormalizesGoMethods(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "FixtureBucket/Describe",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "fixture.go",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "FixtureBucket/Describe")
	require.True(t, ok, "expected normalized method match, got %#v", result.Symbols)
	assert.Equal(t, "fixture.go", symbol.File)
	assert.Equal(t, 29, symbol.StartLine)
	assert.Equal(t, 31, symbol.EndLine)
	assert.Contains(t, symbol.Body, "func (b FixtureBucket[T]) Describe() string")
}

// TestIntegrationServiceFindSymbolNormalizesNestedMethodPathFromParentWorkspaceRoot proves that
// find_symbol keeps the canonical nested-module method path stable even when the workspace root is above the module.
func TestIntegrationServiceFindSymbolNormalizesNestedMethodPathFromParentWorkspaceRoot(t *testing.T) {
	workspaceRoot := goplsParentWorkspaceRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "FixtureBucket/Describe",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         parentWorkspaceFixtureRelativePath("fixture.go"),
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "FixtureBucket/Describe")
	require.True(t, ok, "expected nested-module method match, got %#v", result.Symbols)
	assert.Equal(t, parentWorkspaceFixtureRelativePath("fixture.go"), symbol.File)
	assert.Equal(t, 29, symbol.StartLine)
	assert.Equal(t, 31, symbol.EndLine)
	assert.Contains(t, symbol.Body, "func (b FixtureBucket[T]) Describe() string")
}

// TestIntegrationServiceFindSymbolSupportsPackageQualifiedGoMethodPath proves that package-qualified
// method queries normalize before the shared stdlsp search starts.
func TestIntegrationServiceFindSymbolSupportsPackageQualifiedGoMethodPath(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	for _, path := range []string{"basic/FixtureBucket/Describe", "basic.FixtureBucket/Describe"} {
		result, findErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:        path,
				IncludeBody: true,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "fixture.go",
		})
		require.NoError(t, findErr)

		symbol, ok := findFoundSymbol(result.Symbols, "FixtureBucket/Describe")
		require.Truef(t, ok, "expected normalized package-qualified method match for %q, got %#v", path, result.Symbols)
		assert.Equal(t, "fixture.go", symbol.File)
		assert.Equal(t, 29, symbol.StartLine)
		assert.Equal(t, 31, symbol.EndLine)
		assert.Contains(t, symbol.Body, "func (b FixtureBucket[T]) Describe() string")
	}
}

// TestIntegrationServiceFindSymbolSupportsPackageQualifiedGoPath proves that the Go adapter
// tolerates package-qualified queries even though canonical paths stay package-unqualified.
func TestIntegrationServiceFindSymbolSupportsPackageQualifiedGoPath(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	for _, path := range []string{"basic/MakeBucket", "basic.MakeBucket"} {
		result, findErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{Path: path},
			WorkspaceRoot:    workspaceRoot,
			Scope:            "fixture.go",
		})
		require.NoError(t, findErr)

		symbol, ok := findFoundSymbol(result.Symbols, "MakeBucket")
		require.Truef(t, ok, "expected normalized package-qualified match for %q, got %#v", path, result.Symbols)
		assert.Equal(t, "fixture.go", symbol.File)
		assert.Equal(t, 34, symbol.StartLine)
		assert.Equal(t, 39, symbol.EndLine)
	}
}

// TestIntegrationServiceFindSymbolSupportsPackageQualifiedLowercaseGoPath proves that package-qualified
// lowercase Go functions normalize to the canonical package-unqualified path.
func TestIntegrationServiceFindSymbolSupportsPackageQualifiedLowercaseGoPath(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	for _, path := range []string{"basic/makeBucketPrivate", "basic.makeBucketPrivate"} {
		result, findErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{Path: path},
			WorkspaceRoot:    workspaceRoot,
			Scope:            "fixture.go",
		})
		require.NoError(t, findErr)

		symbol, ok := findFoundSymbol(result.Symbols, "makeBucketPrivate")
		require.Truef(t, ok, "expected normalized lowercase package-qualified match for %q, got %#v", path, result.Symbols)
		assert.Equal(t, "fixture.go", symbol.File)
	}
}

// TestIntegrationServiceFindReferencingSymbolsSupportsPackageQualifiedGoPath proves that the
// Go adapter also normalizes package-qualified target paths for reference lookups.
func TestIntegrationServiceFindReferencingSymbolsSupportsPackageQualifiedGoPath(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	for _, path := range []string{"basic/MakeBucket", "basic.MakeBucket"} {
		result, findErr := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
			FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: path},
			WorkspaceRoot:                workspaceRoot,
			File:                         "fixture.go",
		})
		require.NoError(t, findErr)

		twice, ok := findReferencingSymbol(result.Symbols, "UseMakeBucketTwice")
		require.Truef(t, ok, "expected normalized package-qualified references for %q, got %#v", path, result.Symbols)
		assert.Equal(t, 3, twice.ContentStartLine)
		assert.Equal(t, 5, twice.ContentEndLine)
		assert.Contains(t, twice.Content, "MakeBucket")
		assert.Equal(t, "references.go", twice.File)
	}
}

// TestIntegrationServiceFindReferencingSymbolsSupportsPackageQualifiedLowercaseGoPath proves that
// lowercase package-qualified Go functions normalize before the shared reference lookup starts.
func TestIntegrationServiceFindReferencingSymbolsSupportsPackageQualifiedLowercaseGoPath(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	for _, path := range []string{"basic/makeBucketPrivate", "basic.makeBucketPrivate"} {
		result, findErr := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
			FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: path},
			WorkspaceRoot:                workspaceRoot,
			File:                         "fixture.go",
		})
		require.NoError(t, findErr)

		twice, ok := findReferencingSymbol(result.Symbols, "UseMakeBucketPrivateTwice")
		require.Truef(t, ok, "expected normalized lowercase package-qualified references for %q, got %#v", path, result.Symbols)
		assert.Equal(t, 16, twice.ContentStartLine)
		assert.Equal(t, 18, twice.ContentEndLine)
		assert.Contains(t, twice.Content, "makeBucketPrivate")
		assert.Equal(t, "references.go", twice.File)
	}
}

// TestIntegrationServiceFindReferencingSymbolsSupportsPackageQualifiedGoMethodPath proves that the
// method lookup path accepts package-qualified input before the shared reference workflow starts.
func TestIntegrationServiceFindReferencingSymbolsSupportsPackageQualifiedGoMethodPath(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	for _, path := range []string{"basic/FixtureBucket/Describe", "basic.FixtureBucket/Describe"} {
		result, findErr := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
			FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: path},
			WorkspaceRoot:                workspaceRoot,
			File:                         "fixture.go",
		})
		require.NoError(t, findErr)

		twice, ok := findReferencingSymbol(result.Symbols, "UseMakeBucketTwice")
		require.Truef(t, ok, "expected normalized package-qualified method references for %q, got %#v", path, result.Symbols)
		assert.Equal(t, 4, twice.ContentStartLine)
		assert.Equal(t, 6, twice.ContentEndLine)
		assert.Contains(t, twice.Content, "Describe")
		assert.Equal(t, "references.go", twice.File)
	}
}

// TestIntegrationServiceFindReferencingSymbolsSupportsNestedMethodPathsFromParentWorkspaceRoot proves that
// grouped method references survive the parent-workspace request-document workflow, not only plain function references.
func TestIntegrationServiceFindReferencingSymbolsSupportsNestedMethodPathsFromParentWorkspaceRoot(t *testing.T) {
	workspaceRoot := goplsParentWorkspaceRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path: "FixtureBucket/Describe",
		},
		WorkspaceRoot: workspaceRoot,
		File:          parentWorkspaceFixtureRelativePath("fixture.go"),
	})
	require.NoError(t, err)

	twice, ok := findReferencingSymbol(result.Symbols, "UseMakeBucketTwice")
	require.True(t, ok, "expected nested-module method references, got %#v", result.Symbols)
	assert.Equal(t, 4, twice.ContentStartLine)
	assert.Equal(t, parentWorkspaceFixtureRelativePath("references.go"), twice.File)
	assert.Contains(t, twice.Content, "Describe")
}

// TestIntegrationServiceFindReferencingSymbolsSupportsExactCanonicalGoPath proves that reference lookup
// also accepts a leading slash only for the canonical public Go path.
func TestIntegrationServiceFindReferencingSymbolsSupportsExactCanonicalGoPath(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, findErr := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "/FixtureBucket/Describe"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.go",
	})
	require.NoError(t, findErr)

	twice, ok := findReferencingSymbol(result.Symbols, "UseMakeBucketTwice")
	require.True(t, ok, "expected exact canonical method references, got %#v", result.Symbols)
	assert.Equal(t, 4, twice.ContentStartLine)
	assert.Equal(t, 6, twice.ContentEndLine)
	assert.Contains(t, twice.Content, "Describe")
	assert.Equal(t, "references.go", twice.File)
}

// TestIntegrationServiceFindReferencingSymbolsDoesNotSupportExactPackageQualifiedGoPath proves that
// absolute package-qualified input stays outside the public exact-path contract for reference lookup.
func TestIntegrationServiceFindReferencingSymbolsDoesNotSupportExactPackageQualifiedGoPath(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	for _, path := range []string{"/basic/MakeBucket", "/basic/FixtureBucket/Describe"} {
		result, findErr := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
			FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: path},
			WorkspaceRoot:                workspaceRoot,
			File:                         "fixture.go",
		})
		require.Error(t, findErr)
		assert.ErrorContains(t, findErr, "no symbol matches")
		assert.Emptyf(t, result.Symbols, "expected no references for unsupported exact path %q, got %#v", path, result.Symbols)
	}
}

// TestIntegrationServiceFindSymbolSupportsExactCanonicalGoPath proves that exact lookup with a leading slash
// works only for the canonical public name path.
func TestIntegrationServiceFindSymbolSupportsExactCanonicalGoPath(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, findErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "/FixtureBucket/Describe"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "fixture.go",
	})
	require.NoError(t, findErr)

	_, ok := findFoundSymbol(result.Symbols, "FixtureBucket/Describe")
	require.True(t, ok, "expected exact canonical method match, got %#v", result.Symbols)
}

// TestIntegrationServiceFindSymbolDoesNotSupportExactPackageQualifiedGoPath proves that leading-slash
// exact matching stays reserved for canonical public paths, not package-qualified input variants.
func TestIntegrationServiceFindSymbolDoesNotSupportExactPackageQualifiedGoPath(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	for _, path := range []string{"/basic/MakeBucket", "/basic/FixtureBucket/Describe"} {
		result, findErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{Path: path},
			WorkspaceRoot:    workspaceRoot,
			Scope:            "fixture.go",
		})
		require.NoError(t, findErr)
		assert.Emptyf(t, result.Symbols, "expected no exact match for package-qualified path %q, got %#v", path, result.Symbols)
	}
}

// TestIntegrationServiceFindReferencingSymbolsGroupsReferenceLines proves that live reference lookups
// keep one unique line entry per referencing symbol line while grouping by the containing symbol.
func TestIntegrationServiceFindReferencingSymbolsGroupsReferenceLines(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path: "MakeBucket",
		},
		WorkspaceRoot: workspaceRoot,
		File:          "fixture.go",
	})
	require.NoError(t, err)

	twice, ok := findReferencingSymbol(result.Symbols, "UseMakeBucketTwice")
	require.True(t, ok, "expected grouped references for UseMakeBucketTwice, got %#v", result.Symbols)
	assert.Equal(t, 3, twice.ContentStartLine)
	assert.Equal(t, 5, twice.ContentEndLine)
	assert.Contains(t, twice.Content, "MakeBucket")
	assert.Equal(t, "references.go", twice.File)

	once, ok := findReferencingSymbol(result.Symbols, "UseMakeBucketOnce")
	require.True(t, ok, "expected grouped references for UseMakeBucketOnce, got %#v", result.Symbols)
	assert.Equal(t, 11, once.ContentStartLine)
	assert.Equal(t, 13, once.ContentEndLine)
	assert.Contains(t, once.Content, "MakeBucket")
	assert.Equal(t, "references.go", once.File)
}

// TestIntegrationServiceFindReferencingSymbolsKeepsSingleLineRangeForMultilineContext proves that
// the published flat shape keeps one reference anchor while content may span several lines of context.
func TestIntegrationServiceFindReferencingSymbolsKeepsSingleLineRangeForMultilineContext(t *testing.T) {
	workspaceRoot := goplsMultilineFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path: "MakeBucket",
		},
		WorkspaceRoot: workspaceRoot,
		File:          "fixture.go",
	})
	require.NoError(t, err)

	formatted, ok := findReferencingSymbol(result.Symbols, "UseDescribeAcrossLines")
	require.True(t, ok, "expected multiline formatted reference, got %#v", result.Symbols)
	assert.Equal(t, "references.go", formatted.File)
	assert.Equal(t, 4, formatted.ContentStartLine)
	assert.Equal(t, 6, formatted.ContentEndLine)
	assert.Contains(t, formatted.Content, "return MakeBucket(")
	assert.Contains(t, formatted.Content, "value,")
}

// TestIntegrationRawDocumentSymbolsReturnHierarchicalDocumentSymbols proves that the runtime
// advertises enough client capabilities for gopls to return DocumentSymbol nodes with real selection ranges.
func TestIntegrationRawDocumentSymbolsReturnHierarchicalDocumentSymbols(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	conn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)

	absolutePath := filepath.Join(workspaceRoot, "fixture.go")
	params := &protocol.DocumentSymbolParams{
		WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
		PartialResultParams:    protocol.PartialResultParams{},
		TextDocument: protocol.TextDocumentIdentifier{
			URI: uri.File(absolutePath),
		},
	}

	symbols := rawGoplsDocumentSymbols(t, ctx, conn, params)
	require.Len(t, symbols, 8)

	assert.Equal(t, []string{
		"FixtureStamp",
		"FixtureCounter",
		"FixtureContract",
		"FixtureBucket",
		"(*FixtureBucket[T]).MeasureDepth",
		"(FixtureBucket[T]).Describe",
		"MakeBucket",
		"makeBucketPrivate",
	}, rawDocumentSymbolNames(symbols))

	fixtureContract, ok := findRawDocumentSymbol(symbols, "FixtureContract")
	require.True(t, ok)
	require.Len(t, fixtureContract.Children, 2)
	assert.Equal(t, []string{"MeasureDepth", "Describe"}, rawDocumentSymbolNames(fixtureContract.Children))

	fixtureBucket, ok := findRawDocumentSymbol(symbols, "FixtureBucket")
	require.True(t, ok)
	require.Len(t, fixtureBucket.Children, 2)
	assert.Equal(t, []string{"Label", "Value"}, rawDocumentSymbolNames(fixtureBucket.Children))

	makeBucket, ok := findRawDocumentSymbol(symbols, "MakeBucket")
	require.True(t, ok)
	assert.Equal(t, uint32(34), makeBucket.SelectionRange.Start.Line)
	assert.Equal(t, uint32(5), makeBucket.SelectionRange.Start.Character)

	describe, ok := findRawDocumentSymbol(symbols, "(FixtureBucket[T]).Describe")
	require.True(t, ok)
	assert.Equal(t, uint32(29), describe.SelectionRange.Start.Line)
	assert.Equal(t, uint32(26), describe.SelectionRange.Start.Character)
}

// TestIntegrationFindSymbolWithoutGoNameNormalizationStillNeedsBuildNamePath proves that client
// capabilities alone do not normalize gopls receiver-prefixed method names into canonical type/method paths.
func TestIntegrationFindSymbolWithoutGoNameNormalizationStillNeedsBuildNamePath(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	rawStd := newIntegrationStdService(t, service, nil)

	result, err := rawStd.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "FixtureBucket/Describe",
			IncludeBody: false,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "fixture.go",
	})
	require.NoError(t, err)
	assert.Empty(t, result.Symbols)
}

// TestIntegrationFindMethodReferencesWithoutLookupCallbackWorks proves that method symbols also expose
// a usable identifier selection range after the runtime starts advertising hierarchical document symbols.
func TestIntegrationFindMethodReferencesWithoutLookupCallbackWorks(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	goStd := newIntegrationStdService(t, service, buildNamePath)

	request := &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path: "FixtureBucket/Describe",
		},
		WorkspaceRoot: workspaceRoot,
		File:          "fixture.go",
	}

	result, err := goStd.FindReferencingSymbols(ctx, request)
	require.NoError(t, err)

	twice, ok := findReferencingSymbol(result.Symbols, "UseMakeBucketTwice")
	require.True(t, ok, "expected grouped method references without lookup callback, got %#v", result.Symbols)
	assert.Equal(t, 4, twice.ContentStartLine)
	assert.Equal(t, 6, twice.ContentEndLine)
	assert.Contains(t, twice.Content, "left.Describe()")
	assert.Equal(t, "references.go", twice.File)
}

// TestIntegrationServiceFindReferencingSymbolsSupportsNestedModulePathsFromParentWorkspaceRoot proves that
// a gopls session rooted above the fixture module can still group references for symbols declared inside it.
func TestIntegrationServiceFindReferencingSymbolsSupportsNestedModulePathsFromParentWorkspaceRoot(t *testing.T) {
	workspaceRoot := goplsParentWorkspaceRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path: "MakeBucket",
		},
		WorkspaceRoot: workspaceRoot,
		File:          parentWorkspaceFixtureRelativePath("fixture.go"),
	})
	require.NoError(t, err)

	twice, ok := findReferencingSymbol(result.Symbols, "UseMakeBucketTwice")
	require.True(t, ok, "expected grouped references for UseMakeBucketTwice from parent workspace root, got %#v", result.Symbols)
	assert.Equal(t, 3, twice.ContentStartLine)
	assert.Equal(t, parentWorkspaceFixtureRelativePath("references.go"), twice.File)

	once, ok := findReferencingSymbol(result.Symbols, "UseMakeBucketOnce")
	require.True(t, ok, "expected grouped references for UseMakeBucketOnce from parent workspace root, got %#v", result.Symbols)
	assert.Equal(t, 11, once.ContentStartLine)
	assert.Equal(t, parentWorkspaceFixtureRelativePath("references.go"), once.File)
}

// TestIntegrationServiceFindReferencingSymbolsSupportsTaggedOnlyBuildFlags proves that callers can expose
// symbols declared only behind a custom build tag when they pass explicit gopls build settings.
func TestIntegrationServiceFindReferencingSymbolsSupportsTaggedOnlyBuildFlags(t *testing.T) {
	workspaceRoot := goplsBuildTagsFixtureRoot(t)
	service, ctx := newIntegrationServiceWithConfig(t, cfgadapters.GoplsConfig{
		BuildFlags: []string{"-tags=featurex"},
		Env:        nil,
	})

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path: "TaggedOnly",
		},
		WorkspaceRoot: workspaceRoot,
		File:          "tagged_only_featurex.go",
	})
	require.NoError(t, err)

	twice, ok := findReferencingSymbol(result.Symbols, "UseTaggedOnlyTwice")
	require.Truef(t, ok, "expected tagged-only references, got %#v", result.Symbols)
	assert.Equal(t, "references_featurex.go", twice.File)
	assert.Contains(t, twice.Content, "TaggedOnly")
}

// TestIntegrationServiceFindReferencingSymbolsReturnsBuildConfigMessageForTaggedOnlyFileWithoutBuildFlags proves
// that tagged-only files outside the active Go build graph return one safe public message instead of raw gopls
// metadata errors.
func TestIntegrationServiceFindReferencingSymbolsReturnsBuildConfigMessageForTaggedOnlyFileWithoutBuildFlags(t *testing.T) {
	workspaceRoot := goplsBuildTagsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "TaggedOnly"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "tagged_only_featurex.go",
	})
	require.Error(t, err)
	assert.ErrorContains(
		t,
		err,
		`file_path "tagged_only_featurex.go" is excluded from the active Go build configuration`,
	)
	assert.NotContains(t, err.Error(), "no package metadata for file")
	assert.Empty(t, result.Symbols)
}

// TestIntegrationServiceFindReferencingSymbolsUsesTaggedVariantBuildFlags proves that callers can select
// the correct custom-tagged build variant instead of falling back to the default reference graph.
func TestIntegrationServiceFindReferencingSymbolsUsesTaggedVariantBuildFlags(t *testing.T) {
	workspaceRoot := goplsBuildTagsFixtureRoot(t)
	service, ctx := newIntegrationServiceWithConfig(t, cfgadapters.GoplsConfig{
		BuildFlags: []string{"-tags=featurex"},
		Env:        nil,
	})

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path: "VariantLabel",
		},
		WorkspaceRoot: workspaceRoot,
		File:          "variant_featurex.go",
	})
	require.NoError(t, err)

	variant, ok := findReferencingSymbol(result.Symbols, "UseVariantLabel")
	require.Truef(t, ok, "expected tagged build variant references, got %#v", result.Symbols)
	assert.Equal(t, "variant_refs_featurex.go", variant.File)
	assert.Contains(t, variant.Content, "-featurex")
	assert.NotContains(t, variant.Content, "-default")
}

// TestIntegrationServiceFindSymbolUsesDefaultBuildVariantWithoutBuildFlags proves that
// the default build graph stays observable when callers do not opt into the featurex tag.
func TestIntegrationServiceFindSymbolUsesDefaultBuildVariantWithoutBuildFlags(t *testing.T) {
	workspaceRoot := goplsBuildTagsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "VariantLabel",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "variant_default.go",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "VariantLabel")
	require.True(t, ok, "expected default build variant symbol, got %#v", result.Symbols)
	assert.Equal(t, "variant_default.go", symbol.File)
	assert.Contains(t, symbol.Body, "return \"default\"")
}

// TestIntegrationServiceFindReferencingSymbolsUsesDefaultVariantWithoutBuildFlags proves that
// the untagged reference graph resolves to the default build-tag branch instead of the featurex-only variant.
func TestIntegrationServiceFindReferencingSymbolsUsesDefaultVariantWithoutBuildFlags(t *testing.T) {
	workspaceRoot := goplsBuildTagsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path: "VariantLabel",
		},
		WorkspaceRoot: workspaceRoot,
		File:          "variant_default.go",
	})
	require.NoError(t, err)

	variant, ok := findReferencingSymbol(result.Symbols, "UseVariantLabel")
	require.True(t, ok, "expected default build variant references, got %#v", result.Symbols)
	assert.Equal(t, "variant_refs_default.go", variant.File)
	assert.Contains(t, variant.Content, "-default")
	assert.NotContains(t, variant.Content, "-featurex")
}

// TestIntegrationServiceGetSymbolsOverviewReturnsCrossPackageSharedSymbols proves that
// gopls overview stays package-aware for imported helper packages inside the same fixture module.
func TestIntegrationServiceGetSymbolsOverviewReturnsCrossPackageSharedSymbols(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{
			Depth: 1,
		},
		WorkspaceRoot: workspaceRoot,
		File:          filepath.ToSlash(filepath.Join("crosspkg", "shared", "fixture.go")),
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	sharedFixtureFile := filepath.ToSlash(filepath.Join("crosspkg", "shared", "fixture.go"))
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindStruct), "ImportedBucket", sharedFixtureFile)
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindMethod), "ImportedBucket/Describe", sharedFixtureFile)
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindStruct), "EmbeddedBase", sharedFixtureFile)
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindMethod), "EmbeddedBase/DescribeEmbedded", sharedFixtureFile)
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindInterface), "Contract", sharedFixtureFile)
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindFunction), "MakeImportedBucket", sharedFixtureFile)
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindClass), "AliasBucket", sharedFixtureFile)
}

// TestIntegrationServiceFindSymbolSupportsTypeAliasesAcrossPackages proves that
// canonical symbol lookup can resolve a cross-package type alias that points at a real struct declaration.
func TestIntegrationServiceFindSymbolSupportsTypeAliasesAcrossPackages(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "AliasBucket",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         filepath.ToSlash(filepath.Join("crosspkg", "shared", "fixture.go")),
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "AliasBucket")
	require.True(t, ok, "expected type alias symbol, got %#v", result.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("crosspkg", "shared", "fixture.go")), symbol.File)
	assert.Contains(t, symbol.Body, "AliasBucket = ImportedBucket")
}

// TestIntegrationServiceFindSymbolSupportsCrossPackageFactoryDeclarations proves that
// direct lookup still resolves the shared package factory even when the fixture module contains multiple packages.
func TestIntegrationServiceFindSymbolSupportsCrossPackageFactoryDeclarations(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "MakeImportedBucket",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         filepath.ToSlash(filepath.Join("crosspkg", "shared", "fixture.go")),
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "MakeImportedBucket")
	require.True(t, ok, "expected cross-package factory symbol, got %#v", result.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("crosspkg", "shared", "fixture.go")), symbol.File)
	assert.Contains(t, symbol.Body, "func MakeImportedBucket(label string) ImportedBucket")
}

// TestIntegrationServiceFindSymbolSupportsPromotedMemberDeclarationsAcrossPackages proves that
// canonical lookup resolves the embedded declaration that later becomes a promoted method in another package.
func TestIntegrationServiceFindSymbolSupportsPromotedMemberDeclarationsAcrossPackages(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "EmbeddedBase/DescribeEmbedded",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         filepath.ToSlash(filepath.Join("crosspkg", "shared", "fixture.go")),
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "EmbeddedBase/DescribeEmbedded")
	require.True(t, ok, "expected promoted member declaration symbol, got %#v", result.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("crosspkg", "shared", "fixture.go")), symbol.File)
	assert.Contains(t, symbol.Body, "func (EmbeddedBase) DescribeEmbedded() string")
}

// TestIntegrationServiceFindReferencingSymbolsSupportsAliasImportSelectorsAcrossPackages proves that
// cross-package reference grouping keeps alias-import selectors attached to the imported factory function.
func TestIntegrationServiceFindReferencingSymbolsSupportsAliasImportSelectorsAcrossPackages(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path: "MakeImportedBucket",
		},
		WorkspaceRoot: workspaceRoot,
		File:          filepath.ToSlash(filepath.Join("crosspkg", "shared", "fixture.go")),
	})
	require.NoError(t, err)

	aliasImport, ok := findReferencingSymbol(result.Symbols, "UseAliasImport")
	require.True(t, ok, "expected alias-import selector references, got %#v", result.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("crosspkg", "consumer", "fixture.go")), aliasImport.File)
	assert.Contains(t, aliasImport.Content, "fixturelib.MakeImportedBucket")
}

// TestIntegrationServiceFindReferencingSymbolsSupportsPromotedMembersAcrossPackages proves that
// gopls groups promoted-method references back to the embedded declaration instead of treating them as unrelated selectors.
func TestIntegrationServiceFindReferencingSymbolsSupportsPromotedMembersAcrossPackages(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path: "EmbeddedBase/DescribeEmbedded",
		},
		WorkspaceRoot: workspaceRoot,
		File:          filepath.ToSlash(filepath.Join("crosspkg", "shared", "fixture.go")),
	})
	require.NoError(t, err)

	promoted, ok := findReferencingSymbol(result.Symbols, "UsePromotedMethod")
	require.True(t, ok, "expected promoted member references, got %#v", result.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("crosspkg", "consumer", "fixture.go")), promoted.File)
	assert.Contains(t, promoted.Content, "DescribeEmbedded")
}

// TestIntegrationServiceFindReferencingSymbolsSupportsInterfaceMethodDispatchAcrossPackages proves that
// interface-dispatch references stay attached to the interface method symbol when another package calls through the contract.
func TestIntegrationServiceFindReferencingSymbolsSupportsInterfaceMethodDispatchAcrossPackages(t *testing.T) {
	workspaceRoot := goplsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path: "Contract/Describe",
		},
		WorkspaceRoot: workspaceRoot,
		File:          filepath.ToSlash(filepath.Join("crosspkg", "shared", "fixture.go")),
	})
	require.NoError(t, err)

	dispatch, ok := findReferencingSymbol(result.Symbols, "UseInterfaceDispatch")
	require.True(t, ok, "expected interface dispatch references, got %#v", result.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("crosspkg", "consumer", "fixture.go")), dispatch.File)
	assert.Contains(t, dispatch.Content, "contract.Describe()")
}

// TestIntegrationServiceFindSymbolIncludeInfoUsesNestedModulePkgGoDevLink proves that include_info keeps
// hover links module-aware for symbols declared inside a nested module when gopls runs from the parent workspace root.
func TestIntegrationServiceFindSymbolIncludeInfoUsesNestedModulePkgGoDevLink(t *testing.T) {
	workspaceRoot := goplsParentWorkspaceRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "MakeBucket",
			IncludeInfo: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         parentWorkspaceFixtureRelativePath("fixture.go"),
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "MakeBucket")
	require.True(t, ok, "expected nested-module symbol match, got %#v", result.Symbols)
	assert.Contains(t, symbol.Info, "https://pkg.go.dev/example.com/asteria-gopls-fixture#MakeBucket")
	assert.NotContains(t, symbol.Info, "command-line-arguments")
	assert.True(t, strings.Contains(symbol.Info, "MakeBucket gives the fixture one generic function symbol."))
}

// TestIntegrationWithRequestDocumentRecoversRawReferencesFromParentWorkspaceRoot proves that
// withRequestDocument is the stable adapter-level contract that recovers raw gopls locations
// in the parent-workspace scenario before stdlsp groups them into the final response.
func TestIntegrationWithRequestDocumentRecoversRawReferencesFromParentWorkspaceRoot(t *testing.T) {
	workspaceRoot := goplsParentWorkspaceRoot(t)
	service, ctx := newIntegrationService(t)

	conn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)

	absolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(parentWorkspaceFixtureRelativePath("fixture.go")))
	params := &protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(absolutePath)},
			Position:     protocol.Position{Line: 34, Character: 5},
		},
		WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
		PartialResultParams:    protocol.PartialResultParams{},
		Context:                protocol.ReferenceContext{IncludeDeclaration: false},
	}

	var locations []protocol.Location
	err = helpers.WithRequestDocument(func(_ string) string { return goplsLanguageID })(ctx, conn, absolutePath, func(callCtx context.Context) error {
		locations = rawGoplsReferences(t, callCtx, conn, params)
		return nil
	})
	require.NoError(t, err)
	require.Len(t, locations, 3)
}

// TestIntegrationServiceServesMultipleRoots proves that one gopls adapter instance can serve different roots sequentially.
func TestIntegrationServiceServesMultipleRoots(t *testing.T) {
	service, ctx := newIntegrationService(t)

	firstResult, firstErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "MakeBucket"},
		WorkspaceRoot:    goplsFixtureRoot(t),
		Scope:            "fixture.go",
	})
	require.NoError(t, firstErr)
	_, firstFound := findFoundSymbol(firstResult.Symbols, "MakeBucket")
	require.True(t, firstFound)

	secondResult, secondErr := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "MakeBucket"},
		WorkspaceRoot:                goplsMultilineFixtureRoot(t),
		File:                         "fixture.go",
	})
	require.NoError(t, secondErr)
	formatted, ok := findReferencingSymbol(secondResult.Symbols, "UseDescribeAcrossLines")
	require.True(t, ok)
	assert.Equal(t, "references.go", formatted.File)
}

// TestIntegrationServiceFindSymbolUpdatesRenamedGoFileWithoutRestart proves the public find_symbol contract
// against a live temp workspace: once a Go file is rewritten with a renamed function, the old symbol path must
// disappear and the new one must become searchable without restarting the gopls-backed session.
func TestIntegrationServiceFindSymbolUpdatesRenamedGoFileWithoutRestart(t *testing.T) {
	workspaceRoot := copyGoplsFixtureRoot(t, "basic")
	service, ctx := newIntegrationService(t)

	_, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.go",
	})
	require.NoError(t, err)

	writeGoplsWorkspaceFile(t, workspaceRoot, "watcher_probe.go", "package basic\n\nfunc watcher_added_go_symbol() string {\n\treturn \"added\"\n}\n")

	require.Eventually(t, func() bool {
		result, findErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:        "watcher_added_go_symbol",
				IncludeBody: true,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "watcher_probe.go",
		})
		if findErr != nil {
			return false
		}

		symbol, ok := findFoundSymbol(result.Symbols, "watcher_added_go_symbol")

		return ok && strings.Contains(symbol.Body, `func watcher_added_go_symbol() string`)
	}, goplsLiveWaitTimeout, goplsLiveWaitTick)

	writeGoplsWorkspaceFile(t, workspaceRoot, "watcher_probe.go", "package basic\n\nfunc watcher_renamed_go_symbol() string {\n\treturn \"renamed\"\n}\n")

	require.Eventually(t, func() bool {
		oldResult, oldErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:        "watcher_added_go_symbol",
				IncludeBody: true,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "watcher_probe.go",
		})
		if oldErr != nil {
			return false
		}

		newResult, newErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:        "watcher_renamed_go_symbol",
				IncludeBody: true,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "watcher_probe.go",
		})
		if newErr != nil {
			return false
		}

		_, oldFound := findFoundSymbol(oldResult.Symbols, "watcher_added_go_symbol")
		newSymbol, newFound := findFoundSymbol(newResult.Symbols, "watcher_renamed_go_symbol")

		return !oldFound && newFound && strings.Contains(newSymbol.Body, `func watcher_renamed_go_symbol() string`)
	}, goplsLiveWaitTimeout, goplsLiveWaitTick)
}

// TestIntegrationServiceGetSymbolsOverviewUpdatesRenamedGoFileWithoutRestart proves that overview responses
// publish the rewritten Go function name after an on-disk rename inside the same live temp workspace.
func TestIntegrationServiceGetSymbolsOverviewUpdatesRenamedGoFileWithoutRestart(t *testing.T) {
	workspaceRoot := copyGoplsFixtureRoot(t, "basic")
	service, ctx := newIntegrationService(t)

	_, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.go",
	})
	require.NoError(t, err)

	writeGoplsWorkspaceFile(t, workspaceRoot, "watcher_probe.go", "package basic\n\nfunc watcher_added_go_symbol() string {\n\treturn \"added\"\n}\n")

	require.Eventually(t, func() bool {
		result, overviewErr := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
			WorkspaceRoot:            workspaceRoot,
			File:                     "watcher_probe.go",
		})
		if overviewErr != nil {
			return false
		}

		location, ok := helpers.FindOverviewSymbol(result.Symbols, "watcher_added_go_symbol")

		return ok && location.Kind == int(protocol.SymbolKindFunction)
	}, goplsLiveWaitTimeout, goplsLiveWaitTick)

	writeGoplsWorkspaceFile(t, workspaceRoot, "watcher_probe.go", "package basic\n\nfunc watcher_renamed_go_symbol() string {\n\treturn \"renamed\"\n}\n")

	require.Eventually(t, func() bool {
		result, overviewErr := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
			WorkspaceRoot:            workspaceRoot,
			File:                     "watcher_probe.go",
		})
		if overviewErr != nil {
			return false
		}

		_, oldFound := helpers.FindOverviewSymbol(result.Symbols, "watcher_added_go_symbol")
		newLocation, newFound := helpers.FindOverviewSymbol(result.Symbols, "watcher_renamed_go_symbol")

		return !oldFound && newFound && newLocation.Kind == int(protocol.SymbolKindFunction)
	}, goplsLiveWaitTimeout, goplsLiveWaitTick)
}

// goplsFixtureRoot returns the absolute path to the stable Go module used by integration tests.
func goplsFixtureRoot(t *testing.T) string {
	t.Helper()

	fixtureRoot, err := filepath.Abs(filepath.Join("testdata", "basic"))
	require.NoError(t, err)

	return fixtureRoot
}

// copyGoplsFixtureRoot copies one tracked Go fixture tree into a temp workspace so live integration tests can
// mutate files without touching source-controlled fixtures.
func copyGoplsFixtureRoot(t *testing.T, fixtureName string) string {
	t.Helper()

	trackedRoot := trackedGoplsFixtureRoot(t, fixtureName)
	workspaceRoot := filepath.Join(t.TempDir(), fixtureName)
	require.NotEqual(t, trackedRoot, workspaceRoot)
	require.NoError(t, copyGoplsFixtureTree(trackedRoot, workspaceRoot))

	return workspaceRoot
}

// trackedGoplsFixtureRoot resolves the repository fixture root before tests copy it into temp storage.
func trackedGoplsFixtureRoot(t *testing.T, fixtureName string) string {
	t.Helper()

	fixtureRoot, err := filepath.Abs(filepath.Join("testdata", fixtureName))
	require.NoError(t, err)

	return fixtureRoot
}

// copyGoplsFixtureTree recreates one tracked Go fixture tree under a temp workspace with original permissions.
func copyGoplsFixtureTree(sourceRoot string, destinationRoot string) error {
	return filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relativePath, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}

		if relativePath == "." {
			return os.MkdirAll(destinationRoot, goplsFixtureDirPermissions)
		}

		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}

		destinationPath := filepath.Join(destinationRoot, relativePath)
		if entry.IsDir() {
			return os.MkdirAll(destinationPath, entryInfo.Mode().Perm())
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(destinationPath, content, entryInfo.Mode().Perm())
	})
}

// writeGoplsWorkspaceFile keeps live mutation tests focused on behavior instead of repeated directory setup.
func writeGoplsWorkspaceFile(t *testing.T, workspaceRoot string, relativePath string, content string) {
	t.Helper()

	absolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(relativePath))
	require.NoError(t, os.MkdirAll(filepath.Dir(absolutePath), goplsFixtureDirPermissions))
	require.NoError(t, os.WriteFile(absolutePath, []byte(content), goplsFixtureFilePermissions))
}

// goplsMultilineFixtureRoot returns the absolute path to the Go module that contains multiline
// reference formatting examples for integration tests.
func goplsMultilineFixtureRoot(t *testing.T) string {
	t.Helper()

	fixtureRoot, err := filepath.Abs(filepath.Join("testdata", "multiline"))
	require.NoError(t, err)

	return fixtureRoot
}

// goplsParentWorkspaceRoot returns the absolute path to the parent-workspace fixture that contains
// a nested Go module under testdata.
func goplsParentWorkspaceRoot(t *testing.T) string {
	t.Helper()

	workspaceRoot, err := filepath.Abs(filepath.Join("testdata", "parentworkspace"))
	require.NoError(t, err)

	return workspaceRoot
}

// goplsBuildTagsFixtureRoot returns the absolute path to the Go module that exercises custom build tags.
func goplsBuildTagsFixtureRoot(t *testing.T) string {
	t.Helper()

	workspaceRoot, err := filepath.Abs(filepath.Join("testdata", "buildtags"))
	require.NoError(t, err)

	return workspaceRoot
}

// parentWorkspaceFixtureRelativePath keeps nested-module integration tests readable while still exercising
// parent-workspace lookups.
func parentWorkspaceFixtureRelativePath(fileName string) string {
	return filepath.ToSlash(filepath.Join("nested", "basic", fileName))
}

// overviewKinds extracts the distinct kind keys so integration tests can assert the full returned set.
func overviewKinds(symbols []domain.SymbolLocation) []int {
	kindsByValue := make(map[int]struct{})
	for _, symbol := range symbols {
		kindsByValue[symbol.Kind] = struct{}{}
	}
	kinds := make([]int, 0, len(kindsByValue))
	for kind := range kindsByValue {
		kinds = append(kinds, kind)
	}

	return kinds
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
func findReferencingSymbol(
	symbols []domain.ReferencingSymbol,
	namePath string,
) (domain.ReferencingSymbol, bool) {
	for _, symbol := range symbols {
		if symbol.Path == namePath {
			return symbol, true
		}
	}

	return domain.ReferencingSymbol{}, false
}

// rawGoplsDocumentSymbols reads the live textDocument/documentSymbol response as DocumentSymbol items.
func rawGoplsDocumentSymbols(
	t *testing.T,
	ctx context.Context,
	conn jsonrpc2.Conn,
	params *protocol.DocumentSymbolParams,
) []protocol.DocumentSymbol {
	t.Helper()

	var rawSymbols []json.RawMessage
	err := protocol.Call(ctx, conn, protocol.MethodTextDocumentDocumentSymbol, params, &rawSymbols)
	require.NoError(t, err)
	require.NotEmpty(t, rawSymbols)

	symbols := make([]protocol.DocumentSymbol, 0, len(rawSymbols))
	for _, rawSymbol := range rawSymbols {
		fields := make(map[string]json.RawMessage)
		err = json.Unmarshal(rawSymbol, &fields)
		require.NoError(t, err)
		require.NotContains(t, fields, "location")

		var symbol protocol.DocumentSymbol
		err = json.Unmarshal(rawSymbol, &symbol)
		require.NoError(t, err)
		symbols = append(symbols, symbol)
	}

	return symbols
}

// rawGoplsReferences reads the live textDocument/references response as protocol locations.
func rawGoplsReferences(
	t *testing.T,
	ctx context.Context,
	conn jsonrpc2.Conn,
	params *protocol.ReferenceParams,
) []protocol.Location {
	t.Helper()

	var locations []protocol.Location
	err := protocol.Call(ctx, conn, protocol.MethodTextDocumentReferences, params, &locations)
	require.NoError(t, err)

	return locations
}

// rawDocumentSymbolNames keeps raw-symbol assertions compact and focused on the payload content.
func rawDocumentSymbolNames(symbols []protocol.DocumentSymbol) []string {
	names := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		names = append(names, symbol.Name)
	}

	return names
}

// findRawDocumentSymbol resolves one raw symbol by name so tests can assert the exact returned node shape.
func findRawDocumentSymbol(
	symbols []protocol.DocumentSymbol,
	name string,
) (protocol.DocumentSymbol, bool) {
	for _, symbol := range symbols {
		if symbol.Name == name {
			return symbol, true
		}
	}

	return protocol.DocumentSymbol{}, false
}
