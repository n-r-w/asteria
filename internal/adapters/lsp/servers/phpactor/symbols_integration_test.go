//go:build integration_tests

package lspphpactor

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/n-r-w/asteria/internal/usecase/router"
)

const (
	phpactorFixtureFilePermissions = fs.FileMode(0o600)
	// indexingLoadFileCount is the number of generated classes that keeps phpactor indexing longer than
	// indexingDeadline.
	indexingLoadFileCount = 2000
	// indexingDeadline is the deadline of a reference search that comes while phpactor indexes the project.
	indexingDeadline = 300 * time.Millisecond
)

// TestIntegrationServiceGetSymbolsOverviewReturnsFixtureSymbols proves that the live phpactor-backed overview
// exposes stable top-level and nested PHP symbols from the fixture workspace.
func TestIntegrationServiceGetSymbolsOverviewReturnsFixtureSymbols(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.php",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindClass), "Bucket")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindConstant), "FIXTURE_STAMP")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindProperty), "Bucket/value")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindMethod), "Bucket/describe")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindFunction), "make_bucket")
}

// TestIntegrationServiceCanceledReferenceRequestClosesWorkspaceSession proves that a PHP reference tool request
// canceled during its search does not leave its language-server process available for reuse after the request returns.
func TestIntegrationServiceCanceledReferenceRequestClosesWorkspaceSession(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	firstConn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)

	// The request is canceled when the search opens the target file, after the wait for indexing.
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	openDocument := service.withRequestDocument
	service.withRequestDocument = func(
		ctx context.Context,
		conn jsonrpc2.Conn,
		absolutePath string,
		run func(context.Context) error,
	) error {
		cancel()

		return openDocument(ctx, conn, absolutePath, run)
	}
	_, err = service.FindReferencingSymbols(requestCtx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "make_bucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.php",
	})
	require.ErrorIs(t, err, context.Canceled)

	restartedConn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)
	assert.NotSame(t, firstConn, restartedConn)
}

// TestIntegrationServiceReferenceSearchOfNewSessionOpensOnlyTarget proves that a reference search that starts the
// phpactor session finds a reference in another file while the adapter opens no file except the target.
func TestIntegrationServiceReferenceSearchOfNewSessionOpensOnlyTarget(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	var (
		mu          sync.Mutex
		openedFiles []string
	)
	openDocument := service.withRequestDocument
	service.withRequestDocument = func(
		ctx context.Context,
		conn jsonrpc2.Conn,
		absolutePath string,
		run func(context.Context) error,
	) error {
		mu.Lock()
		openedFiles = append(openedFiles, filepath.Base(absolutePath))
		mu.Unlock()

		return openDocument(ctx, conn, absolutePath, run)
	}

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "make_bucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.php",
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "use_bucket")
	require.True(t, ok, "expected reference in another file, got %#v", result.Symbols)
	assert.Equal(t, "references.php", symbol.File)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"fixture.php"}, openedFiles)
}

// TestIntegrationServiceReferenceDeadlineDuringIndexingKeepsSession proves that a reference search whose deadline
// comes while phpactor indexes the project returns the deadline error and leaves the session running, so the next
// search reuses it.
func TestIntegrationServiceReferenceDeadlineDuringIndexingKeepsSession(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	writeIndexingLoadFixture(t, workspaceRoot)
	service, ctx := newIntegrationService(t)
	request := &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "make_bucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.php",
	}

	firstConn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)

	requestCtx, cancel := context.WithTimeout(ctx, indexingDeadline)
	defer cancel()
	_, err = service.FindReferencingSymbols(requestCtx, request)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	sameConn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)
	assert.Same(t, firstConn, sameConn)

	result, err := service.FindReferencingSymbols(ctx, request)
	require.NoError(t, err)
	_, ok := findReferencingSymbol(result.Symbols, "use_bucket")
	assert.True(t, ok, "expected reference after indexing, got %#v", result.Symbols)
}

// TestIntegrationServiceDoesNotWriteAutoConfigIntoWorkspace proves that a phpactor session over a project whose
// composer.lock lists tools that phpactor can configure writes no .phpactor.json into the project.
func TestIntegrationServiceDoesNotWriteAutoConfigIntoWorkspace(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, "composer.json"),
		[]byte(`{"require-dev": {"phpstan/phpstan": "^1.10", "phpspec/prophecy": "^1.17"}}`),
		phpactorFixtureFilePermissions,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspaceRoot, "composer.lock"),
		[]byte(`{"packages": [], "packages-dev": [`+
			`{"name": "phpstan/phpstan", "version": "1.10.0"}, `+
			`{"name": "phpspec/prophecy", "version": "1.17.0"}]}`),
		phpactorFixtureFilePermissions,
	))
	service, ctx := newIntegrationService(t)

	// The reference search waits until phpactor finishes indexing, which happens after its startup configuration.
	_, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "make_bucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.php",
	})
	require.NoError(t, err)

	assert.NoFileExists(t, filepath.Join(workspaceRoot, ".phpactor.json"))
}

// TestIntegrationPropertyReferencesCommandHonorsContextCancellation proves that the CLI fallback cannot outlive a
// canceled MCP tool request.
func TestIntegrationPropertyReferencesCommandHonorsContextCancellation(t *testing.T) {
	requestCtx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := runPHPActorPropertyReferences(requestCtx, phpactorFixtureRoot(t), t.TempDir(), "Bucket", "value")
	require.ErrorIs(t, err, context.Canceled)
}

// TestIntegrationServiceGetSymbolsOverviewReturnsNamespacedDeclarations proves that the live phpactor-backed
// overview exposes namespaced PHP declarations, traits, enums, inherited members, and static members from
// the existing fixture root without needing a separate workspace layout.
func TestIntegrationServiceGetSymbolsOverviewReturnsNamespacedDeclarations(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 3},
		WorkspaceRoot:            workspaceRoot,
		File:                     "namespaced.php",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	assertOverviewContainsExactPathInFile(t, result.Symbols, "namespaced.php", "BucketFormatter")
	assertOverviewContainsExactPathInFile(t, result.Symbols, "namespaced.php", "BucketLabeling")
	assertOverviewContainsExactPathInFile(t, result.Symbols, "namespaced.php", "BucketKind")
	assertOverviewContainsExactPathInFile(t, result.Symbols, "namespaced.php", "BucketKind/Primary")
	assertOverviewContainsExactPathInFile(t, result.Symbols, "namespaced.php", "BaseBucket/inheritedLabel")
	assertOverviewContainsExactPathInFile(t, result.Symbols, "namespaced.php", "AdvancedBucket")
	assertOverviewContainsExactPathInFile(t, result.Symbols, "namespaced.php", "AdvancedBucket/KIND_PREFIX")
	assertOverviewContainsExactPathInFile(t, result.Symbols, "namespaced.php", "AdvancedBucket/createTagged")
	assertOverviewContainsExactPathInFile(t, result.Symbols, "namespaced.php", "BucketLabeling/DEFAULT_PREFIX")
	assertOverviewContainsExactPathInFile(t, result.Symbols, "namespaced.php", "BucketLabeling/describeStatic")
}

// TestIntegrationServiceGetSymbolsOverviewReturnsSecondaryWorkerSymbol proves that the second fixture root
// exercises observable phpactor symbol behavior instead of only runtime-session isolation.
func TestIntegrationServiceGetSymbolsOverviewReturnsSecondaryWorkerSymbol(t *testing.T) {
	workspaceRoot := phpactorSecondaryFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "worker.php",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	assertOverviewContainsExactPathInFile(t, result.Symbols, "worker.php", "run_worker")
}

// TestIntegrationServiceFindSymbolReturnsCanonicalClassMethod proves that canonical PHP class-member paths
// resolve through stdlsp without adapter-specific query normalization.
func TestIntegrationServiceFindSymbolReturnsCanonicalClassMethod(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "Bucket/describe"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "fixture.php",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "Bucket/describe")
	require.True(t, ok, "expected method match, got %#v", result.Symbols)
	assert.Equal(t, "fixture.php", symbol.File)
}

// TestIntegrationServiceFindSymbolReturnsCanonicalPropertyDeclaration proves that exact property lookup stays
// observable because phpactor's property-reference fallback depends on resolving the declaration target.
func TestIntegrationServiceFindSymbolReturnsCanonicalPropertyDeclaration(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "Bucket/value"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "fixture.php",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "Bucket/value")
	require.True(t, ok, "expected property match, got %#v", result.Symbols)
	assert.Equal(t, int(protocol.SymbolKindProperty), symbol.Kind)
	assert.Equal(t, "fixture.php", symbol.File)
}

// TestIntegrationServiceFindSymbolReturnsTopLevelConstant proves that phpactor exposes file-level constants
// through the same canonical find_symbol workflow as the other supported languages.
func TestIntegrationServiceFindSymbolReturnsTopLevelConstant(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "/FIXTURE_STAMP"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "fixture.php",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "FIXTURE_STAMP")
	require.True(t, ok, "expected constant match, got %#v", result.Symbols)
	assert.Equal(t, int(protocol.SymbolKindConstant), symbol.Kind)
	assert.Equal(t, "fixture.php", symbol.File)
}

// TestIntegrationServiceFindSymbolReturnsTypedNamespacedBodies proves that phpactor-backed symbol lookup keeps
// modern PHP declarations observable through stable bodies for promoted properties, typed signatures,
// attributes, closures, and anonymous classes.
func TestIntegrationServiceFindSymbolReturnsTypedNamespacedBodies(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	constructorResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "AdvancedBucket/__construct",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "namespaced.php",
	})
	require.NoError(t, err)

	constructorSymbol, ok := findFoundSymbol(constructorResult.Symbols, "AdvancedBucket/__construct")
	require.True(t, ok, "expected constructor match, got %#v", constructorResult.Symbols)
	assert.Equal(t, "namespaced.php", constructorSymbol.File)
	assert.Contains(t, constructorSymbol.Body, "private readonly string $value")
	assert.Contains(t, constructorSymbol.Body, "?BucketKind $kind = null")

	typedResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "AdvancedBucket/createTagged",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "namespaced.php",
	})
	require.NoError(t, err)

	typedSymbol, ok := findFoundSymbol(typedResult.Symbols, "AdvancedBucket/createTagged")
	require.True(t, ok, "expected typed factory match, got %#v", typedResult.Symbols)
	assert.Contains(t, typedSymbol.Body, "string|Stringable $value")
	assert.Contains(t, typedSymbol.Body, "Countable&Stringable $tag")

	interfaceResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "BucketFormatter/format",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "namespaced.php",
	})
	require.NoError(t, err)

	interfaceSymbol, ok := findFoundSymbol(interfaceResult.Symbols, "BucketFormatter/format")
	require.True(t, ok, "expected interface member match, got %#v", interfaceResult.Symbols)
	assert.Contains(t, interfaceSymbol.Body, "function format(string $value): string;")

	traitResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "BucketLabeling/DEFAULT_PREFIX",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "namespaced.php",
	})
	require.NoError(t, err)

	traitSymbol, ok := findFoundSymbol(traitResult.Symbols, "BucketLabeling/DEFAULT_PREFIX")
	require.True(t, ok, "expected trait constant match, got %#v", traitResult.Symbols)
	assert.Contains(t, traitSymbol.Body, "PREFIX = 'bucket'")

	enumResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "BucketKind/Primary",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "namespaced.php",
	})
	require.NoError(t, err)

	enumSymbol, ok := findFoundSymbol(enumResult.Symbols, "BucketKind/Primary")
	require.True(t, ok, "expected enum case match, got %#v", enumResult.Symbols)
	assert.Contains(t, enumSymbol.Body, "Primary = 'primary';")

	constantResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "AdvancedBucket/KIND_PREFIX",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "namespaced.php",
	})
	require.NoError(t, err)

	constantSymbol, ok := findFoundSymbol(constantResult.Symbols, "AdvancedBucket/KIND_PREFIX")
	require.True(t, ok, "expected class constant match, got %#v", constantResult.Symbols)
	assert.Contains(t, constantSymbol.Body, "PREFIX = 'kind'")

	classResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "AdvancedBucket",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "namespaced.php",
	})
	require.NoError(t, err)

	classSymbol, ok := findFoundSymbol(classResult.Symbols, "AdvancedBucket")
	require.True(t, ok, "expected namespaced class match, got %#v", classResult.Symbols)
	assert.Contains(t, classSymbol.Body, "#[Marker]")

	describeResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "AdvancedBucket/describe",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "namespaced.php",
	})
	require.NoError(t, err)

	describeSymbol, ok := findFoundSymbol(describeResult.Symbols, "AdvancedBucket/describe")
	require.True(t, ok, "expected describe match, got %#v", describeResult.Symbols)
	assert.Contains(t, describeSymbol.Body, "function (string $suffix)")
	assert.Contains(t, describeSymbol.Body, "new class($closure)")
}

// TestIntegrationServiceFindSymbolReturnsUsefulClassInfo proves that class info stays meaningful when
// phpactor-backed find_symbol asks for optional hover metadata.
func TestIntegrationServiceFindSymbolReturnsUsefulClassInfo(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "/Bucket",
			IncludeInfo: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "fixture.php",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "Bucket")
	require.True(t, ok, "expected class match, got %#v", result.Symbols)
	assert.NotEmpty(t, symbol.Info)
	assert.NotContains(t, symbol.Info, "Could not find source")
	assert.Contains(t, symbol.Info, "class Bucket")
}

// TestIntegrationServiceFindSymbolReturnsUsefulDocblockInfo proves that a docblock-bearing PHP symbol keeps
// `IncludeInfo` usable even when the meaningful description lives in the docblock and the adapter may need to
// fall back to the declaration line.
func TestIntegrationServiceFindSymbolReturnsUsefulDocblockInfo(t *testing.T) {
	const (
		docblockSummary = "Turns loose bucket input into a stable printable label."
		declarationLine = "function describe_docblock_bucket($value)"
	)

	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "describe_docblock_bucket",
			IncludeInfo: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "docblock.php",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "describe_docblock_bucket")
	require.True(t, ok, "expected docblock-bearing function match, got %#v", result.Symbols)
	assert.Equal(t, "docblock.php", symbol.File)
	assert.NotEmpty(t, symbol.Info)
	assert.NotContains(t, symbol.Info, "Could not find source")
	assert.True(
		t,
		strings.Contains(symbol.Info, docblockSummary) || strings.Contains(symbol.Info, declarationLine),
		"expected info to contain docblock summary %q or declaration line %q, got %q",
		docblockSummary,
		declarationLine,
		symbol.Info,
	)
}

// TestIntegrationServiceFindSymbolUsesDirectoryFilter proves that workspace-wide PHP search excludes
// node_modules and cache while still leaving vendor code searchable.
func TestIntegrationServiceFindSymbolUsesDirectoryFilter(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	writeIgnoredNodeModulesFixture(t, workspaceRoot)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "directory_filter_target"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "",
	})
	require.NoError(t, err)

	assert.Equal(
		t,
		[]string{"fixture.php", filepath.ToSlash(filepath.Join("vendor", "vendor_helper.php"))},
		collectFoundSymbolFiles(result.Symbols),
	)
}

// TestIntegrationServiceFindSymbolUsesDirectoryFilterWithPersistedIndex proves that workspace-wide PHP search
// still ignores cache and node_modules even when the workspace already contains a persisted phpactor index.
func TestIntegrationServiceFindSymbolUsesDirectoryFilterWithPersistedIndex(t *testing.T) {
	workspaceRoot := phpactorFixtureRootWithLocalState(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "directory_filter_target"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "",
	})
	require.NoError(t, err)

	assert.Equal(
		t,
		[]string{"fixture.php", filepath.ToSlash(filepath.Join("vendor", "vendor_helper.php"))},
		collectFoundSymbolFiles(result.Symbols),
	)
}

// TestIntegrationRouterFindSymbolUsesDirectoryFilterWithPersistedIndex proves that the public router/tool path
// keeps phpactor's ignored-directory rules intact during workspace-wide search with a persisted local index.
func TestIntegrationRouterFindSymbolUsesDirectoryFilterWithPersistedIndex(t *testing.T) {
	workspaceRoot := phpactorFixtureRootWithLocalState(t)
	service, ctx := newIntegrationService(t)

	search, err := router.New([]router.ILSP{service})
	require.NoError(t, err)

	result, err := search.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "directory_filter_target"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "",
	})
	require.NoError(t, err)

	assert.Equal(
		t,
		[]string{"fixture.php", filepath.ToSlash(filepath.Join("vendor", "vendor_helper.php"))},
		collectFoundSymbolFiles(result.Symbols),
	)
}

// TestIntegrationServiceFindSymbolReturnsSymbolsOfFileWithInvalidUTF8 proves that workspace-wide PHP search
// reads a file whose string literal holds bytes that are not valid UTF-8, as generated protobuf metadata does.
func TestIntegrationServiceFindSymbolReturnsSymbolsOfFileWithInvalidUTF8(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "BinaryMetadata/descriptor"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "",
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"binary_metadata.php"}, collectFoundSymbolFiles(result.Symbols))
}

// TestIntegrationServiceFindReferencingSymbolsGroupsReferences proves that live reference lookup groups PHP
// usages by the smallest containing symbol exposed in the referencing file.
func TestIntegrationServiceFindReferencingSymbolsGroupsReferences(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "make_bucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.php",
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "use_bucket")
	require.True(t, ok, "expected grouped references, got %#v", result.Symbols)
	assert.Equal(t, "references.php", symbol.File)
	assert.Contains(t, symbol.Content, "make_bucket('secondary')")
}

// TestIntegrationServiceFindReferencingSymbolsReturnsClassConstructorCalls proves that phpactor-backed
// class reference lookup includes constructor call sites in other files.
func TestIntegrationServiceFindReferencingSymbolsReturnsClassConstructorCalls(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "Bucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.php",
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "use_bucket")
	require.True(t, ok, "expected grouped class reference, got %#v", result.Symbols)
	assert.Equal(t, "references.php", symbol.File)
	assert.Contains(t, symbol.Content, "new Bucket('primary')")
}

// TestIntegrationServiceFindReferencingSymbolsReturnsConstructorCalls proves that constructor-path lookups
// include cross-file `new` expressions instead of only same-file constructor call sites.
func TestIntegrationServiceFindReferencingSymbolsReturnsConstructorCalls(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "Bucket/__construct"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.php",
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "use_bucket")
	require.True(t, ok, "expected grouped constructor reference, got %#v", result.Symbols)
	assert.Equal(t, "references.php", symbol.File)
	assert.Contains(t, symbol.Content, "new Bucket('primary')")
}

// TestIntegrationServiceFindReferencingSymbolsReturnsPropertyUsageContainers proves that property reference
// lookup groups usages under the containing methods where the field is actually read or written.
func TestIntegrationServiceFindReferencingSymbolsReturnsPropertyUsageContainers(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "Bucket/value"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.php",
	})
	require.NoError(t, err)
	assert.Len(t, result.Symbols, 2)

	_, declarationPresent := findReferencingSymbol(result.Symbols, "Bucket/value")
	assert.False(t, declarationPresent)

	constructorSymbol, ok := findReferencingSymbol(result.Symbols, "Bucket/__construct")
	require.True(t, ok, "expected constructor property reference, got %#v", result.Symbols)
	assert.Equal(t, "fixture.php", constructorSymbol.File)
	assert.Contains(t, constructorSymbol.Content, "$this->value = $value")

	describeSymbol, ok := findReferencingSymbol(result.Symbols, "Bucket/describe")
	require.True(t, ok, "expected describe property reference, got %#v", result.Symbols)
	assert.Equal(t, "fixture.php", describeSymbol.File)
	assert.Contains(t, describeSymbol.Content, "return $this->value")
}

// TestIntegrationServicePropertyFallbackUsesManagedIndex proves that the property reference fallback finds usages
// through the index in the managed cache and writes nothing into phpactor's default index directory.
func TestIntegrationServicePropertyFallbackUsesManagedIndex(t *testing.T) {
	// phpactor places its default cache, and the default index in it, under XDG_CACHE_HOME.
	cacheHome := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cacheHome)
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	var fallbackRows []phpactorReferenceRow
	runPropertyReferences := service.runPropertyReferences
	service.runPropertyReferences = func(
		ctx context.Context,
		workspaceRoot string,
		indexPath string,
		referenceTarget string,
		propertyName string,
	) ([]phpactorReferenceRow, error) {
		rows, err := runPropertyReferences(ctx, workspaceRoot, indexPath, referenceTarget, propertyName)
		fallbackRows = rows

		return rows, err
	}

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "Bucket/value"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.php",
	})
	require.NoError(t, err)

	assert.NotEmpty(t, fallbackRows, "expected property usages from the phpactor command")
	_, ok := findReferencingSymbol(result.Symbols, "Bucket/describe")
	assert.True(t, ok, "expected describe property reference, got %#v", result.Symbols)
	assert.NoDirExists(t, filepath.Join(cacheHome, "phpactor", "index"))
	normalizedRoot, err := helpers.ResolveWorkspaceRoot(workspaceRoot)
	require.NoError(t, err)
	indexPath, err := phpactorIndexerPath(service.cacheRoot, normalizedRoot)
	require.NoError(t, err)
	indexEntries, err := os.ReadDir(indexPath)
	require.NoError(t, err)
	assert.NotEmpty(t, indexEntries)
}

// TestIntegrationServiceFindReferencingSymbolsUsesDeclarationFileForProjectLikePropertyLookup proves that the
// phpactor property fallback stays usable for namespaced files stored under project-like directory layouts where
// direct class-name lookup fails but declaration-file lookup succeeds.
func TestIntegrationServiceFindReferencingSymbolsUsesDeclarationFileForProjectLikePropertyLookup(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "Bucket/value"},
		WorkspaceRoot:                workspaceRoot,
		File:                         filepath.ToSlash(filepath.Join("src", "Acme", "Model", "Bucket.php")),
	})
	require.NoError(t, err)
	assert.Len(t, result.Symbols, 2)

	constructorSymbol, ok := findReferencingSymbol(result.Symbols, "Bucket/__construct")
	require.True(t, ok, "expected constructor property reference, got %#v", result.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("src", "Acme", "Model", "Bucket.php")), constructorSymbol.File)
	assert.Contains(t, constructorSymbol.Content, "$this->value = $value")

	describeSymbol, ok := findReferencingSymbol(result.Symbols, "Bucket/describe")
	require.True(t, ok, "expected describe property reference, got %#v", result.Symbols)
	assert.Equal(t, filepath.ToSlash(filepath.Join("src", "Acme", "Model", "Bucket.php")), describeSymbol.File)
	assert.Contains(t, describeSymbol.Content, "return $this->value")
}

// TestIntegrationServiceFindReferencingSymbolsResolvesNamespacedAliasUsage proves that grouped references stay
// correct when phpactor resolves namespaced declarations through `use` aliases in a separate file.
func TestIntegrationServiceFindReferencingSymbolsResolvesNamespacedAliasUsage(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "AdvancedBucket/createTagged"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "namespaced.php",
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "use_namespaced_bucket")
	require.True(t, ok, "expected grouped alias reference, got %#v", result.Symbols)
	assert.Equal(t, "namespaced_references.php", symbol.File)
	assert.Contains(t, symbol.Content, "ImportedBucket::createTagged")

	constantResult, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "AdvancedBucket/KIND_PREFIX"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "namespaced.php",
	})
	require.NoError(t, err)

	constantSymbol, ok := findReferencingSymbol(constantResult.Symbols, "use_namespaced_bucket")
	require.True(t, ok, "expected grouped class-constant reference, got %#v", constantResult.Symbols)
	assert.Equal(t, "namespaced_references.php", constantSymbol.File)
	assert.Contains(t, constantSymbol.Content, "ImportedBucket::KIND_PREFIX")
}

// TestIntegrationServiceConcurrentSameRootReusesSingleConnection proves that concurrent callers for one
// workspace root share the same live runtime connection.
func TestIntegrationServiceConcurrentSameRootReusesSingleConnection(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
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
	firstRoot := phpactorFixtureRoot(t)
	secondRoot := phpactorSecondaryFixtureRoot(t)
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

// TestIntegrationServiceConcurrentInvalidRootDoesNotPoisonValidRoot proves that one bad workspace root
// does not break a concurrent successful request on a different root.
func TestIntegrationServiceConcurrentInvalidRootDoesNotPoisonValidRoot(t *testing.T) {
	workspaceRoot := phpactorFixtureRoot(t)
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
			File:                     "fixture.php",
		})
		validResults <- overviewResult{result: result, err: err}
	})

	waitGroup.Go(func() {
		<-start
		_, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
			WorkspaceRoot:            filepath.Join(workspaceRoot, "missing-root"),
			File:                     "fixture.php",
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

	location := assertOverviewContainsExactPathInFile(t, symbols, "fixture.php", path)
	assert.Equal(t, kind, location.Kind)
}

// assertOverviewContainsExactPathInFile keeps overview assertions reusable when phpactor fixtures live in
// multiple files under the same workspace root.
func assertOverviewContainsExactPathInFile(
	t *testing.T,
	symbols []domain.SymbolLocation,
	expectedFile string,
	path string,
) domain.SymbolLocation {
	t.Helper()

	location, ok := helpers.FindOverviewSymbol(symbols, path)
	require.Truef(t, ok, "expected %q in overview, got: %#v", path, symbols)
	assert.Equal(t, expectedFile, location.File)
	assert.GreaterOrEqual(t, location.StartLine, 0)
	assert.GreaterOrEqual(t, location.EndLine, location.StartLine)

	return location
}

// newIntegrationService keeps repeated phpactor service setup out of individual integration scenarios.
func newIntegrationService(t *testing.T) (*Service, context.Context) {
	t.Helper()

	service, err := New(t.TempDir(), 0)
	require.NoError(t, err)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, service.Close(ctx))
	})

	return service, ctx
}

// phpactorFixtureRoot returns the absolute path to the stable PHP fixture project used by integration tests.
func phpactorFixtureRoot(t *testing.T) string {
	t.Helper()

	requirePHPActorInstalled(t)

	return copyPHPActorFixtureRoot(t, "basic")
}

// phpactorFixtureRootWithLocalState returns the fixture project with tracked phpactor state preserved so
// integration tests can reproduce behavior that depends on a pre-existing local index.
func phpactorFixtureRootWithLocalState(t *testing.T) string {
	t.Helper()

	requirePHPActorInstalled(t)

	return copyPHPActorFixtureRootWithLocalState(t, "basic")
}

// phpactorSecondaryFixtureRoot returns the absolute path to the secondary PHP fixture project used by multi-root tests.
func phpactorSecondaryFixtureRoot(t *testing.T) string {
	t.Helper()

	requirePHPActorInstalled(t)

	return copyPHPActorFixtureRoot(t, "secondary")
}

// copyPHPActorFixtureRoot copies the tracked fixture tree into a temp workspace so live Phpactor runs never
// write state back into the repository tree.
func copyPHPActorFixtureRoot(t *testing.T, fixtureName string) string {
	t.Helper()

	return copyPHPActorFixtureRootWithMode(t, fixtureName, false)
}

// copyPHPActorFixtureRootWithLocalState copies one tracked fixture tree into a temp workspace while preserving
// the checked-in local phpactor state used to reproduce stale-index scenarios.
func copyPHPActorFixtureRootWithLocalState(t *testing.T, fixtureName string) string {
	t.Helper()

	return copyPHPActorFixtureRootWithMode(t, fixtureName, true)
}

// copyPHPActorFixtureRootWithMode keeps the temp-workspace setup shared between clean and local-state fixtures.
func copyPHPActorFixtureRootWithMode(t *testing.T, fixtureName string, preserveLocalState bool) string {
	t.Helper()

	trackedRoot := trackedPHPActorFixtureRoot(t, fixtureName)
	workspaceRoot := filepath.Join(t.TempDir(), fixtureName)
	require.NotEqual(t, trackedRoot, workspaceRoot)
	require.NoError(t, copyPHPActorFixtureTree(trackedRoot, workspaceRoot, preserveLocalState))

	return workspaceRoot
}

// copyPHPActorFixtureTree recreates the tracked fixture files under a temp workspace while skipping local-only
// state that would make integration validation depend on a developer's checkout.
func copyPHPActorFixtureTree(sourceRoot string, destinationRoot string, preserveLocalState bool) error {
	return filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relativePath, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}

		if relativePath == "." {
			return os.MkdirAll(destinationRoot, phpactorStateDirPermissions)
		}

		if shouldSkipPHPActorFixtureEntry(relativePath, preserveLocalState) {
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

// shouldSkipPHPActorFixtureEntry keeps generated Phpactor state and local-only ignored directories out of the
// temp workspace copy so tests prove behavior without depending on checkout residue.

func shouldSkipPHPActorFixtureEntry(relativePath string, preserveLocalState bool) bool {
	if preserveLocalState {
		return false
	}

	switch filepath.Base(relativePath) {
	case ".phpactor", "node_modules":
		return true
	default:
		return false
	}
}

// trackedPHPActorFixtureRoot returns the repository fixture root so integration tests can prove they are using
// isolated temp workspaces instead of mutating source-controlled directories.
func trackedPHPActorFixtureRoot(t *testing.T, fixtureName string) string {
	t.Helper()

	fixtureRoot, err := filepath.Abs(filepath.Join("testdata", fixtureName))
	require.NoError(t, err)

	return fixtureRoot
}

// requirePHPActorInstalled fails fast with a clear message when the external phpactor prerequisite is missing.
func requirePHPActorInstalled(t *testing.T) {
	t.Helper()

	_, err := exec.LookPath(phpactorServerName)
	require.NoError(t, err, "phpactor must be installed and available in PATH")

	_, err = exec.LookPath("php")
	require.NoError(t, err, "php runtime must be installed and available in PATH for phpactor")
}

// writeIgnoredNodeModulesFixture creates a live PHP file under node_modules so the directory-filter test proves
// exclusion against the runtime workspace scan instead of relying on local repository state.
func writeIgnoredNodeModulesFixture(t *testing.T, workspaceRoot string) {
	t.Helper()

	ignoredFile := filepath.Join(workspaceRoot, "node_modules", "ignored", "ignored.php")
	require.NoError(t, os.MkdirAll(filepath.Dir(ignoredFile), phpactorStateDirPermissions))
	require.NoError(
		t,
		os.WriteFile(
			ignoredFile,
			[]byte("<?php\n\nfunction directory_filter_target(): string\n{\n    return 'node_modules';\n}\n"),
			phpactorFixtureFilePermissions,
		),
	)
}

// writeIndexingLoadFixture adds indexingLoadFileCount generated classes to the workspace so that phpactor indexing
// lasts longer than indexingDeadline.
func writeIndexingLoadFixture(t *testing.T, workspaceRoot string) {
	t.Helper()

	loadDir := filepath.Join(workspaceRoot, "generated")
	require.NoError(t, os.MkdirAll(loadDir, phpactorStateDirPermissions))
	for i := range indexingLoadFileCount {
		className := fmt.Sprintf("GeneratedLoad%d", i)
		content := fmt.Sprintf(
			"<?php\n\nclass %s\n{\n    public function read(): string\n    {\n        return '%s';\n    }\n}\n",
			className,
			className,
		)
		require.NoError(
			t,
			os.WriteFile(filepath.Join(loadDir, className+".php"), []byte(content), phpactorFixtureFilePermissions),
		)
	}
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

// collectFoundSymbolFiles keeps directory-filter assertions focused on the files that survived traversal.
func collectFoundSymbolFiles(symbols []domain.FoundSymbol) []string {
	files := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		files = append(files, symbol.File)
	}

	return files
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
