//go:build integration_tests

package lsptsls

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
)

// TestIntegrationServiceGetSymbolsOverviewReturnsFixtureSymbols proves that the live TSLS-backed overview
// exposes the expected top-level and nested TypeScript symbols from the stable fixture project.
func TestIntegrationServiceGetSymbolsOverviewReturnsFixtureSymbols(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.ts",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindInterface), "FixtureContract")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindMethod), "FixtureContract/describe")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindEnum), "FixtureStatus")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindConstant), "FixtureStatus/Active")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindConstant), "FixtureStatus/Archived")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindConstant), "FIXTURE_STAMP")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindVariable), "fixtureCounter")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindClass), "FixtureBucket")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindConstructor), "FixtureBucket/constructor")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindProperty), "FixtureBucket/label")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindProperty), "FixtureBucket/status")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindMethod), "FixtureBucket/describe")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindMethod), "FixtureBucket/bump")
	assertKindContainsExactPath(t, result.Symbols, int(protocol.SymbolKindFunction), "makeBucket")
}

// TestIntegrationServiceFindSymbolReturnsMethodBody proves that canonical class-member paths resolve
// through the shared stdlsp workflow and can return source body text.
func TestIntegrationServiceFindSymbolReturnsMethodBody(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "FixtureBucket/describe",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "fixture.ts",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "FixtureBucket/describe")
	require.True(t, ok, "expected method match, got %#v", result.Symbols)
	assert.Equal(t, "fixture.ts", symbol.File)
	assert.Equal(t, 19, symbol.StartLine)
	assert.Equal(t, 21, symbol.EndLine)
	assert.Contains(t, symbol.Body, "public describe(): string")
}

// TestIntegrationServiceFindSymbolIncludeInfoReturnsHover proves that TSLS hover text is available
// through include_info when stdlsp resolves the target symbol under a request-scoped open document.
func TestIntegrationServiceFindSymbolIncludeInfoReturnsHover(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "makeBucket",
			IncludeInfo: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "fixture.ts",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "makeBucket")
	require.True(t, ok, "expected function match, got %#v", result.Symbols)
	assert.Equal(t, "fixture.ts", symbol.File)
	assert.NotEmpty(t, symbol.Info)
	assert.Contains(t, symbol.Info, "makeBucket")
}

// TestIntegrationServiceFindReferencingSymbolsGroupsReferences proves that live reference lookup groups
// matches by the smallest containing symbol that TSLS exposes in the referenced file.
func TestIntegrationServiceFindReferencingSymbolsGroupsReferences(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "makeBucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.ts",
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "useBucket/right")
	require.True(t, ok, "expected grouped references, got %#v", result.Symbols)
	assert.Equal(t, "references.ts", symbol.File)
	assert.Equal(t, 3, symbol.ContentStartLine)
	assert.Equal(t, 5, symbol.ContentEndLine)
	assert.Contains(t, symbol.Content, "makeBucket(\"secondary\")")
}

// TestIntegrationServiceFindSymbolSupportsJavaScriptFile proves that the adapter uses the correct language ID
// for `.js` files and can still resolve symbols through the shared standard workflow.
func TestIntegrationServiceFindSymbolSupportsJavaScriptFile(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "jsHelper"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "fixture.js",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "jsHelper")
	require.True(t, ok, "expected JavaScript function match, got %#v", result.Symbols)
	assert.Equal(t, "fixture.js", symbol.File)
	assert.Equal(t, 0, symbol.StartLine)
	assert.Equal(t, 2, symbol.EndLine)
}

// TestIntegrationServiceFindSymbolSupportsJavaScriptConstant proves that JavaScript constants remain
// addressable through the shared lookup flow, not only JavaScript functions.
func TestIntegrationServiceFindSymbolSupportsJavaScriptConstant(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "JS_STAMP"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "fixture.js",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "JS_STAMP")
	require.True(t, ok, "expected JavaScript constant match, got %#v", result.Symbols)
	assert.Equal(t, "fixture.js", symbol.File)
	assert.Equal(t, 4, symbol.StartLine)
	assert.Equal(t, 4, symbol.EndLine)
}

// TestIntegrationServiceFindSymbolSupportsAdditionalFileExtensions proves that every extra extension
// declared in the adapter stays discoverable through overview and direct lookup.
func TestIntegrationServiceFindSymbolSupportsAdditionalFileExtensions(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	testCases := []struct {
		Name         string
		File         string
		Path         string
		Kind         int
		BodyContains string
	}{
		{
			Name:         "typescript react",
			File:         "component.tsx",
			Path:         "FixtureBadge",
			Kind:         int(protocol.SymbolKindFunction),
			BodyContains: "new ModuleBucket(label)",
		},
		{
			Name:         "javascript react",
			File:         "component.jsx",
			Path:         "JsxBadge",
			Kind:         int(protocol.SymbolKindFunction),
			BodyContains: "String(label).trim()",
		},
		{
			Name:         "module typescript",
			File:         "module_fixture.mts",
			Path:         "mtsHelper",
			Kind:         int(protocol.SymbolKindFunction),
			BodyContains: "moduleStamp",
		},
		{
			Name:         "commonjs typescript",
			File:         "module_fixture.cts",
			Path:         "ctsHelper",
			Kind:         int(protocol.SymbolKindFunction),
			BodyContains: "toUpperCase",
		},
		{
			Name:         "module javascript",
			File:         "module_fixture.mjs",
			Path:         "mjsHelper",
			Kind:         int(protocol.SymbolKindFunction),
			BodyContains: "String(value).trim()",
		},
		{
			Name:         "commonjs javascript",
			File:         "module_fixture.cjs",
			Path:         "cjsHelper",
			Kind:         int(protocol.SymbolKindFunction),
			BodyContains: "toUpperCase",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.Name, func(t *testing.T) {
			overview, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
				GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
				WorkspaceRoot:            workspaceRoot,
				File:                     testCase.File,
			})
			require.NoError(t, err)
			assertKindContainsExactPathInFile(t, overview.Symbols, testCase.Kind, testCase.Path, testCase.File)

			result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
				FindSymbolFilter: domain.FindSymbolFilter{Path: testCase.Path, IncludeBody: true},
				WorkspaceRoot:    workspaceRoot,
				Scope:            testCase.File,
			})
			require.NoError(t, err)

			symbol, ok := findFoundSymbol(result.Symbols, testCase.Path)
			require.True(t, ok, "expected match for %q in %s, got %#v", testCase.Path, testCase.File, result.Symbols)
			assert.Equal(t, testCase.File, symbol.File)
			assert.Contains(t, symbol.Body, testCase.BodyContains)
		})
	}
}

// TestIntegrationServiceGetSymbolsOverviewReturnsAdvancedTypeScriptSymbols proves that richer
// TypeScript module and type shapes stay visible through the overview surface.
func TestIntegrationServiceGetSymbolsOverviewReturnsAdvancedTypeScriptSymbols(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 2},
		WorkspaceRoot:            workspaceRoot,
		File:                     "advanced.ts",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	assertOverviewContainsExactPathInFile(t, result.Symbols, "ExtendedShape", "advanced.ts")
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindInterface), "LoaderOptions", "advanced.ts")
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindClass), "DerivedBucket", "advanced.ts")
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindMethod), "DerivedBucket/describe", "advanced.ts")
	assert.Len(t, collectTopLevelOverviewPathsWithPrefix(result.Symbols, "createAdvancedBucket@"), 3)
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindFunction), "loadBuckets", "advanced.ts")
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindModule), "AdvancedRegistry", "advanced.ts")
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindConstant), "AdvancedRegistry/current", "advanced.ts")
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindModule), "FixtureAmbient", "advanced.ts")
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindInterface), "FixtureAmbient/Config", "advanced.ts")
}

// TestIntegrationServiceGetSymbolsOverviewReturnsModuleReexportSymbols proves that tsls-specific synthetic
// alias handling exposes re-export declarations even when the live server omits them from documentSymbol.
func TestIntegrationServiceGetSymbolsOverviewReturnsModuleReexportSymbols(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "module_reexports.ts",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindClass), "defaultBucket", "module_reexports.ts")
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindFunction), "aliasBucket", "module_reexports.ts")
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindConstant), "defaultLabel", "module_reexports.ts")
	assertKindContainsExactPathInFile(t, result.Symbols, int(protocol.SymbolKindInterface), "ReexportedShape", "module_reexports.ts")
}

// TestIntegrationServiceFindSymbolReturnsAdvancedTypeScriptBodies proves that the richer fixture keeps
// overloads, decorators, inheritance, async flow, and destructuring visible through symbol bodies.
func TestIntegrationServiceFindSymbolReturnsAdvancedTypeScriptBodies(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	extendedShapeResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "ExtendedShape", IncludeBody: true},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "advanced.ts",
	})
	require.NoError(t, err)

	extendedShapeSymbol, ok := findFoundSymbol(extendedShapeResult.Symbols, "ExtendedShape")
	require.True(t, ok, "expected type alias match, got %#v", extendedShapeResult.Symbols)
	assert.Equal(t, "advanced.ts", extendedShapeSymbol.File)
	assert.Contains(t, extendedShapeSymbol.Body, "type ExtendedShape<TLabel extends string> = ReexportedShape<TLabel> & {")
	assert.Contains(t, extendedShapeSymbol.Body, "readonly note?: string;")

	loaderOptionsResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "LoaderOptions", IncludeBody: true},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "advanced.ts",
	})
	require.NoError(t, err)

	loaderOptionsSymbol, ok := findFoundSymbol(loaderOptionsResult.Symbols, "LoaderOptions")
	require.True(t, ok, "expected interface match, got %#v", loaderOptionsResult.Symbols)
	assert.Equal(t, "advanced.ts", loaderOptionsSymbol.File)
	assert.Contains(t, loaderOptionsSymbol.Body, "readonly label?: string;")
	assert.Contains(t, loaderOptionsSymbol.Body, "readonly count?: number;")

	classResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "DerivedBucket", IncludeBody: true},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "advanced.ts",
	})
	require.NoError(t, err)

	classSymbol, ok := findFoundSymbol(classResult.Symbols, "DerivedBucket")
	require.True(t, ok, "expected advanced class match, got %#v", classResult.Symbols)
	assert.Equal(t, "advanced.ts", classSymbol.File)
	assert.Contains(t, classSymbol.Body, "extends ReexportedBucket<string>")
	assert.Contains(t, classSymbol.Body, "@logged")

	overloadResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "createAdvancedBucket", IncludeBody: true},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "advanced.ts",
	})
	require.NoError(t, err)

	createAdvancedBucketBodies := collectFoundSymbolBodiesWithPrefix(overloadResult.Symbols, "createAdvancedBucket@")
	require.Len(t, createAdvancedBucketBodies, 3, "expected three exact-path overload bodies, got %#v", overloadResult.Symbols)
	assert.Contains(t, createAdvancedBucketBodies[0], "createAdvancedBucket(label: string)")
	assert.Contains(t, createAdvancedBucketBodies[1], "createAdvancedBucket(shape: ExtendedShape<string>)")
	assert.Contains(t, createAdvancedBucketBodies[2], `const { label } = typeof input === "string" ? { label: input } : input`)

	asyncResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "loadBuckets", IncludeBody: true},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "advanced.ts",
	})
	require.NoError(t, err)

	asyncSymbol, ok := findFoundSymbol(asyncResult.Symbols, "loadBuckets")
	require.True(t, ok, "expected async function match, got %#v", asyncResult.Symbols)
	assert.Equal(t, "advanced.ts", asyncSymbol.File)
	assert.Contains(t, asyncSymbol.Body, "export async function loadBuckets")
	assert.Contains(t, asyncSymbol.Body, "moduleNamespace.defaultLabel")
	assert.Contains(t, asyncSymbol.Body, "const createFromNamespace = () => moduleNamespace.aliasBucket(label)")
	assert.Contains(t, asyncSymbol.Body, "await Promise.resolve")
}

// TestIntegrationServiceFindSymbolReturnsModuleReexportBodies proves that synthetic re-export declarations
// stay searchable in the alias file and return the alias statement body instead of the source definition body.
func TestIntegrationServiceFindSymbolReturnsModuleReexportBodies(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	defaultAliasResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "defaultBucket", IncludeBody: true, IncludeInfo: true},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "module_reexports.ts",
	})
	require.NoError(t, err)

	defaultAliasSymbol, ok := findFoundSymbol(defaultAliasResult.Symbols, "defaultBucket")
	require.True(t, ok, "expected default re-export alias match, got %#v", defaultAliasResult.Symbols)
	assert.Equal(t, "module_reexports.ts", defaultAliasSymbol.File)
	assert.Contains(t, defaultAliasSymbol.Body, "export { default as defaultBucket")
	assert.NotEmpty(t, defaultAliasSymbol.Info)
	assert.Contains(t, defaultAliasSymbol.Info, "defaultBucket")

	shapeAliasResult, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "ReexportedShape", IncludeBody: true},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "module_reexports.ts",
	})
	require.NoError(t, err)

	shapeAliasSymbol, ok := findFoundSymbol(shapeAliasResult.Symbols, "ReexportedShape")
	require.True(t, ok, "expected type re-export alias match, got %#v", shapeAliasResult.Symbols)
	assert.Equal(t, "module_reexports.ts", shapeAliasSymbol.File)
	assert.Contains(t, shapeAliasSymbol.Body, "export type { ModuleShape as ReexportedShape }")
}

// TestIntegrationServiceFindReferencingSymbolsTracksAdvancedModuleImports proves that richer module
// flows stay referenceable through direct imports, alias imports, and namespace imports.
func TestIntegrationServiceFindReferencingSymbolsTracksAdvancedModuleImports(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	defaultLabelRefs, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "defaultLabel"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "module_contracts.ts",
	})
	require.NoError(t, err)

	badgeSymbol, ok := findReferencingSymbol(defaultLabelRefs.Symbols, "FixtureBadge")
	require.True(t, ok, "expected TSX defaultLabel reference group, got %#v", defaultLabelRefs.Symbols)
	assert.Equal(t, "component.tsx", badgeSymbol.File)
	assert.Contains(t, badgeSymbol.Content, "label = defaultLabel")

	loadBucketsSymbol, ok := findReferencingSymbol(defaultLabelRefs.Symbols, "loadBuckets")
	require.True(t, ok, "expected namespace-import reference group, got %#v", defaultLabelRefs.Symbols)
	assert.Equal(t, "advanced.ts", loadBucketsSymbol.File)
	assert.Contains(t, loadBucketsSymbol.Content, "moduleNamespace.defaultLabel")

	moduleFactoryRefs, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "createModuleBucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "module_contracts.ts",
	})
	require.NoError(t, err)

	registrySymbol, ok := findReferencingSymbol(moduleFactoryRefs.Symbols, "AdvancedRegistry/current")
	require.True(t, ok, "expected alias-import reference group, got %#v", moduleFactoryRefs.Symbols)
	assert.Equal(t, "advanced.ts", registrySymbol.File)
	assert.Contains(t, registrySymbol.Content, "buildModuleBucket(moduleNamespace.defaultLabel).label")
}

// TestIntegrationServiceFindReferencingSymbolsResolvesModuleReexportTargets proves that alias declarations in
// module_reexports.ts can be used as targets even when tsls itself only exposes references from the source symbol.
func TestIntegrationServiceFindReferencingSymbolsResolvesModuleReexportTargets(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	testCases := []struct {
		name          string
		path          string
		expectedPath  string
		expectedFile  string
		contentNeedle string
	}{
		{
			name:          "default export alias",
			path:          "defaultBucket",
			expectedPath:  "DerivedBucket",
			expectedFile:  "advanced.ts",
			contentNeedle: "extends ReexportedBucket<string>",
		},
		{
			name:          "named export alias",
			path:          "aliasBucket",
			expectedPath:  "loadBuckets/createFromNamespace",
			expectedFile:  "advanced.ts",
			contentNeedle: "moduleNamespace.aliasBucket(label)",
		},
		{
			name:          "type re-export alias",
			path:          "ReexportedShape",
			expectedPath:  "ExtendedShape",
			expectedFile:  "advanced.ts",
			contentNeedle: "ReexportedShape<TLabel>",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
				FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: testCase.path},
				WorkspaceRoot:                workspaceRoot,
				File:                         "module_reexports.ts",
			})
			require.NoError(t, err)

			symbol, ok := findReferencingSymbol(result.Symbols, testCase.expectedPath)
			require.True(t, ok, "expected re-export target references, got %#v", result.Symbols)
			assert.Equal(t, testCase.expectedFile, symbol.File)
			assert.Contains(t, symbol.Content, testCase.contentNeedle)
		})
	}
}

// TestIntegrationServiceFindSymbolSupportsExplicitDirectoryScope proves that an explicit directory scope
// still opens each searched file on demand and returns matches across the scoped folder.
func TestIntegrationServiceFindSymbolSupportsExplicitDirectoryScope(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "useBucket"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            ".",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "useBucket")
	require.True(t, ok, "expected directory-scoped match, got %#v", result.Symbols)
	assert.Equal(t, "references.ts", symbol.File)
	assert.Equal(t, 2, symbol.StartLine)
	assert.Equal(t, 7, symbol.EndLine)
}

// TestIntegrationServiceFindSymbolSupportsWholeWorkspaceScope proves that an empty scope searches the
// whole workspace root and still resolves matches from every supported file type.
func TestIntegrationServiceFindSymbolSupportsWholeWorkspaceScope(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "jsHelper"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "jsHelper")
	require.True(t, ok, "expected whole-workspace match, got %#v", result.Symbols)
	assert.Equal(t, "fixture.js", symbol.File)
	assert.Equal(t, 0, symbol.StartLine)
	assert.Equal(t, 2, symbol.EndLine)
}

// TestIntegrationServiceFindSymbolSupportsSecondaryWorkspaceSymbols proves that a separate fixture root
// still resolves its own exported symbols without leaking the primary workspace contents.
func TestIntegrationServiceFindSymbolSupportsSecondaryWorkspaceSymbols(t *testing.T) {
	workspaceRoot := tslsSecondaryFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "secondaryValue", IncludeBody: true},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "fixture.ts",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "secondaryValue")
	require.True(t, ok, "expected secondary fixture symbol, got %#v", result.Symbols)
	assert.Equal(t, "fixture.ts", symbol.File)
	assert.Contains(t, symbol.Body, `return "secondary"`)
}

// TestIntegrationServiceFindSymbolRejectsScopeOutsideWorkspace proves that stdlsp keeps scope validation
// consistent after TypeScript stops managing request-file lifecycle itself.
func TestIntegrationServiceFindSymbolRejectsScopeOutsideWorkspace(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "makeBucket"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "../outside.ts",
	})
	require.EqualError(t, err, `relative path "../outside.ts" escapes workspace root`)
	assert.Empty(t, result.Symbols)
}

// TestIntegrationServiceFindReferencingSymbolsReturnsCrossFileReferences proves that the tsls-local
// multi-file context keeps cross-file class references visible in the final grouped result.
func TestIntegrationServiceFindReferencingSymbolsReturnsCrossFileReferences(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "FixtureBucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.ts",
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "useBucket/left")
	require.True(t, ok, "expected cross-file grouped references, got %#v", result.Symbols)
	assert.Equal(t, "references.ts", symbol.File)
	assert.Equal(t, 2, symbol.ContentStartLine)
	assert.Equal(t, 4, symbol.ContentEndLine)
	assert.Contains(t, symbol.Content, "new FixtureBucket")
}

// TestIntegrationServiceFindReferencingSymbolsResolvesDuplicateLocalPaths proves that duplicate
// local TypeScript symbols become uniquely addressable by the path returned from overview.
func TestIntegrationServiceFindReferencingSymbolsResolvesDuplicateLocalPaths(t *testing.T) {
	workspaceRoot := tslsDuplicateLocalsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	overview, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.ts",
	})
	require.NoError(t, err)

	duplicatePaths := collectOverviewPathsWithPrefix(overview.Symbols, "execute/notification@")
	require.Len(t, duplicatePaths, 2, "expected two uniquely addressable local notifications, got %#v", overview.Symbols)
	require.NotEqual(t, duplicatePaths[0], duplicatePaths[1])

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "/" + duplicatePaths[0]},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.ts",
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "execute")
	require.True(t, ok, "expected execute reference group, got %#v", result.Symbols)
	assert.Equal(t, "fixture.ts", symbol.File)
	assert.Contains(t, symbol.Content, "notification.message")
}

// TestIntegrationServiceFindSymbolResolvesDuplicateLocalPaths proves that the exact duplicate-local
// path exported by overview also round-trips through find_symbol.
func TestIntegrationServiceFindSymbolResolvesDuplicateLocalPaths(t *testing.T) {
	workspaceRoot := tslsDuplicateLocalsFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	overview, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.ts",
	})
	require.NoError(t, err)

	duplicatePaths := collectOverviewPathsWithPrefix(overview.Symbols, "execute/notification@")
	require.Len(t, duplicatePaths, 2, "expected two uniquely addressable local notifications, got %#v", overview.Symbols)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "/" + duplicatePaths[0]},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "fixture.ts",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, duplicatePaths[0])
	require.True(t, ok, "expected duplicate-local exact-path match, got %#v", result.Symbols)
	assert.Equal(t, "fixture.ts", symbol.File)
	assert.Equal(t, duplicatePaths[0], symbol.Path)
}

// TestIntegrationServiceConcurrentSameRootReusesSingleConnection proves that concurrent callers for one
// workspace root share the same live runtime connection.
func TestIntegrationServiceConcurrentSameRootReusesSingleConnection(t *testing.T) {
	workspaceRoot := tslsFixtureRoot(t)
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
	firstRoot := tslsFixtureRoot(t)
	secondRoot := tslsSecondaryFixtureRoot(t)
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
	workspaceRoot := tslsFixtureRoot(t)
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
			File:                     "fixture.ts",
		})
		validResults <- overviewResult{result: result, err: err}
	}()

	waitGroup.Go(func() {
		<-start
		_, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
			WorkspaceRoot:            filepath.Join(workspaceRoot, "missing-root"),
			File:                     "fixture.ts",
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
func assertKindContainsExactPath(
	t *testing.T,
	symbols []domain.SymbolLocation,
	kind int,
	path string,
) {
	assertKindContainsExactPathInFile(t, symbols, kind, path, "fixture.ts")
}

// assertKindContainsExactPathInFile keeps symbol overview assertions exact even when fixtures span files.
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

// assertOverviewContainsExactPathInFile keeps overview assertions readable when the symbol kind can vary.
func assertOverviewContainsExactPathInFile(
	t *testing.T,
	symbols []domain.SymbolLocation,
	path string,
	file string,
) {
	t.Helper()

	location, ok := helpers.FindOverviewSymbol(symbols, path)
	require.Truef(t, ok, "expected %q in overview, got: %#v", path, symbols)
	assert.Equal(t, file, location.File)
	assert.GreaterOrEqual(t, location.StartLine, 0)
	assert.GreaterOrEqual(t, location.EndLine, location.StartLine)
}

// newIntegrationService keeps repeated TSLS service setup out of individual integration scenarios.
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

// tslsFixtureRoot returns the absolute path to the stable TypeScript fixture project used by integration tests.
func tslsFixtureRoot(t *testing.T) string {
	t.Helper()

	return tslsFixtureRootByName(t, "basic")
}

// tslsSecondaryFixtureRoot returns the absolute path to the secondary TypeScript fixture project used by multi-root tests.
func tslsSecondaryFixtureRoot(t *testing.T) string {
	t.Helper()

	return tslsFixtureRootByName(t, "secondary")
}

// tslsDuplicateLocalsFixtureRoot returns the fixture project that reproduces duplicate local names in one container.
func tslsDuplicateLocalsFixtureRoot(t *testing.T) string {
	t.Helper()

	return tslsFixtureRootByName(t, "duplicate_locals")
}

// tslsFixtureRootByName keeps fixture bootstrap logic in one place while each wrapper still names its scenario.
func tslsFixtureRootByName(t *testing.T, fixtureName string) string {
	t.Helper()

	requireTSLSInstalled(t)
	fixtureRoot := trackedTSLSFixtureRoot(t, fixtureName)
	ensureFixtureDependenciesInstalled(t, fixtureRoot)

	return fixtureRoot
}

// copyTSLSFixtureRoot copies one tracked fixture tree into a temp workspace so raw wire-level integration
// checks do not inherit filesystem state from earlier package tests.
func copyTSLSFixtureRoot(t *testing.T, fixtureName string) string {
	t.Helper()

	trackedRoot := tslsFixtureRootByName(t, fixtureName)
	workspaceRoot := filepath.Join(t.TempDir(), fixtureName)
	require.NotEqual(t, trackedRoot, workspaceRoot)
	require.NoError(t, copyTSLSFixtureTree(trackedRoot, workspaceRoot))

	return workspaceRoot
}

// copyTSLSFixtureTree recreates the tracked fixture files under a temp workspace so live tsls integration
// checks can run without sharing mutable fixture state across the package.
func copyTSLSFixtureTree(sourceRoot string, destinationRoot string) error {
	return filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relativePath, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}

		if relativePath == "." {
			return os.MkdirAll(destinationRoot, 0o755)
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

// trackedTSLSFixtureRoot returns the repository fixture root so helper code can choose explicitly between the
// shared tracked workspace and an isolated temp copy.
func trackedTSLSFixtureRoot(t *testing.T, fixtureName string) string {
	t.Helper()

	return absoluteFixturePath(t, filepath.Join("testdata", fixtureName))
}

// absoluteFixturePath resolves one fixture directory relative to the adapter package.
func absoluteFixturePath(t *testing.T, relativePath string) string {
	t.Helper()

	fixtureRoot, err := filepath.Abs(relativePath)
	require.NoError(t, err)

	return fixtureRoot
}

// requireTSLSInstalled fails fast with a clear message when the external TypeScript language server prerequisite is missing.
func requireTSLSInstalled(t *testing.T) {
	t.Helper()

	_, err := exec.LookPath(tslsServerName)
	require.NoError(t, err, "typescript-language-server must be installed and available in PATH")
}

// ensureFixtureDependenciesInstalled keeps fixture setup explicit while leaving LSP server installation to the environment.
func ensureFixtureDependenciesInstalled(t *testing.T, fixtureRoot string) {
	t.Helper()

	tsserverPath := filepath.Join(fixtureRoot, "node_modules", "typescript", "lib", "tsserver.js")
	if _, err := os.Stat(tsserverPath); err == nil {
		return
	}

	command := exec.CommandContext(t.Context(), "npm", "install", "--no-package-lock", "--no-audit", "--no-fund")
	command.Dir = fixtureRoot
	output, err := command.CombinedOutput()
	require.NoErrorf(t, err, "npm install failed in %s: %s", fixtureRoot, output)
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

// collectOverviewPathsWithPrefix keeps duplicate-local integration assertions focused on exported symbol paths.
func collectOverviewPathsWithPrefix(symbols []domain.SymbolLocation, prefix string) []string {
	paths := make([]string, 0)
	for _, symbol := range symbols {
		if strings.HasPrefix(symbol.Path, prefix) {
			paths = append(paths, symbol.Path)
		}
	}

	return paths
}

// collectTopLevelOverviewPathsWithPrefix keeps overload assertions scoped to the exact symbol paths,
// excluding nested locals returned under the implementation body.
func collectTopLevelOverviewPathsWithPrefix(symbols []domain.SymbolLocation, prefix string) []string {
	paths := make([]string, 0)
	for _, symbol := range symbols {
		if !strings.HasPrefix(symbol.Path, prefix) {
			continue
		}
		if strings.Contains(strings.TrimPrefix(symbol.Path, prefix), "/") {
			continue
		}
		paths = append(paths, symbol.Path)
	}

	return paths
}

// collectFoundSymbolBodiesWithPrefix keeps overload assertions focused on the exact-path results TSLS returns.
func collectFoundSymbolBodiesWithPrefix(symbols []domain.FoundSymbol, prefix string) []string {
	bodies := make([]string, 0)
	for _, symbol := range symbols {
		if strings.HasPrefix(symbol.Path, prefix) {
			bodies = append(bodies, symbol.Body)
		}
	}

	return bodies
}
