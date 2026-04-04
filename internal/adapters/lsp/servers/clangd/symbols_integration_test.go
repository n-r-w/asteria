//go:build integration_tests

package lspclangd

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	lsphelpers "github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

const (
	clangdFixtureDirPermissions  = 0o755
	clangdFixtureFilePermissions = 0o600
	clangdLiveWaitTimeout        = 10 * time.Second
	clangdLiveWaitTick           = 50 * time.Millisecond
)

// TestIntegrationServiceGetSymbolsOverviewReturnsFixtureSymbols proves that the live clangd-backed adapter
// exposes stable C++ declarations from the fixture workspace and materializes the managed compile database copy.
func TestIntegrationServiceGetSymbolsOverviewReturnsFixtureSymbols(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "basic")
	service, ctx, _ := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "greeter.hpp",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	assertOverviewContainsExactPath(t, result.Symbols, "Greeter")
	assertOverviewContainsExactPath(t, result.Symbols, "Greeter/greet")
	assertOverviewContainsExactPath(t, result.Symbols, "call_greeter")

	cacheDir, err := service.cacheDir(workspaceRoot)
	require.NoError(t, err)
	_, statErr := os.Stat(filepath.Join(cacheDir, compileCommandsFileName))
	require.NoError(t, statErr)
}

// TestIntegrationServiceGetSymbolsOverviewReturnsAdvancedCPPSymbols proves that
// the richer C++ fixture exposes namespaces, inheritance, templates, overloads,
// constructors, destructors, and static members through clangd.
func TestIntegrationServiceGetSymbolsOverviewReturnsAdvancedCPPSymbols(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "advanced_cpp")
	service, ctx, _ := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 3},
		WorkspaceRoot:            workspaceRoot,
		File:                     "advanced.hpp",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	for _, path := range []string{
		"demo",
		"demo/WidgetKind",
		"demo/Payload",
		"demo/Box",
		"demo/clamp_value",
		"demo/Identifiable",
		"demo/Identifiable/id",
		"demo/Widget",
		"demo/Widget/Widget",
		"demo/Widget/~Widget",
		"demo/Widget/id",
		"demo/Widget/scale",
		"demo/Widget/create",
		"demo/make_box",
	} {
		assertOverviewContainsExactPath(t, result.Symbols, path)
	}

	operateMatches := collectOverviewPathsWithPrefix(result.Symbols, "demo/operate")
	assert.Len(t, operateMatches, 2)
}

// TestIntegrationServiceGetSymbolsOverviewReturnsCppmSymbols proves that the
// adapter can inspect a supported `.cppm` file extension through clangd.
func TestIntegrationServiceGetSymbolsOverviewReturnsCppmSymbols(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "advanced_cpp")
	service, ctx, _ := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 2},
		WorkspaceRoot:            workspaceRoot,
		File:                     "modules.cppm",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	assertOverviewContainsExactPath(t, result.Symbols, "demo_math")
	assertOverviewContainsExactPath(t, result.Symbols, "demo_math/add")
}

// TestIntegrationServiceUsesExternalClangdCompilationDatabase proves that the
// adapter does not block a valid external `.clangd` CompilationDatabase when no
// local compile_commands.json or compile_flags.txt exists in the workspace root.
func TestIntegrationServiceUsesExternalClangdCompilationDatabase(t *testing.T) {
	workspaceRoot := copyClangdFixtureRoot(t, "macro_external_compdb")
	buildRoot := filepath.Join(t.TempDir(), "macro_external_compdb-build")
	writeMacroExternalCompileCommands(t, workspaceRoot, buildRoot)
	writeClangdWorkspaceFile(
		t,
		workspaceRoot,
		".clangd",
		fmt.Sprintf("CompileFlags:\n  CompilationDatabase: %s\n", filepath.ToSlash(buildRoot)),
	)

	service, ctx, _ := newIntegrationService(t)

	overviewResult, overviewErr := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 2},
		WorkspaceRoot:            workspaceRoot,
		File:                     "src/model.cpp",
	})
	require.NoError(t, overviewErr)
	assertOverviewContainsExactPath(t, overviewResult.Symbols, "MacroModel::MacroModel")
	assertOverviewContainsExactPath(t, overviewResult.Symbols, "MacroModel::~MacroModel")
	assertOverviewContainsExactPath(t, overviewResult.Symbols, "MacroModel::setFastMode")

	findResult, findErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "MacroModel::setFastMode",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "src/model.cpp",
	})
	require.NoError(t, findErr)

	symbol, ok := findFoundSymbol(findResult.Symbols, "MacroModel::setFastMode")
	require.True(t, ok, "expected setFastMode match, got %#v", findResult.Symbols)
	assert.Contains(t, symbol.Body, "value_ = 1")

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		referencesResult, referencesErr := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
			FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "MacroModel/setFastMode"},
			WorkspaceRoot:                workspaceRoot,
			File:                         "include/model.h",
		})
		assert.NoError(collect, referencesErr)

		referencingSymbol, found := findReferencingSymbol(referencesResult.Symbols, "use_model")
		if !assert.Truef(collect, found, "expected use_model reference container, got %#v", referencesResult.Symbols) {
			return
		}

		assert.Equal(collect, "src/use.cpp", referencingSymbol.File)
		assert.Contains(collect, referencingSymbol.Content, "model.setFastMode()")
	}, 5*time.Second, 200*time.Millisecond)
}

// TestIntegrationServiceFindSymbolReturnsFunctionBody proves that clangd-backed symbol lookup returns the
// function definition body from the translation unit backed by the managed compile database.
func TestIntegrationServiceFindSymbolReturnsFunctionBody(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "basic")
	service, ctx, _ := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "/call_greeter",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "greeter.cpp",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "call_greeter")
	require.True(t, ok, "expected function match, got %#v", result.Symbols)
	assert.Equal(t, "greeter.cpp", symbol.File)
	assert.Contains(t, symbol.Body, "return greeter.greet();")
}

// TestIntegrationServiceFindSymbolReturnsAdvancedCPPBodies proves that richer
// C++ declarations stay queryable through clangd-backed body lookup.
func TestIntegrationServiceFindSymbolReturnsAdvancedCPPBodies(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "advanced_cpp")
	service, ctx, _ := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "demo/make_box",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "advanced.cpp",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "demo/make_box")
	require.True(t, ok, "expected function match, got %#v", result.Symbols)
	assert.Contains(t, symbol.Body, "return Box{.width = width, .height = height};")

	templateResult, templateErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "demo/clamp_value",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "advanced.hpp",
	})
	require.NoError(t, templateErr)

	templateSymbol, ok := findFoundSymbol(templateResult.Symbols, "demo/clamp_value")
	require.True(t, ok, "expected template function match, got %#v", templateResult.Symbols)
	assert.Contains(t, templateSymbol.Body, "return value;")
}

// TestIntegrationServiceFindSymbolReturnsOverloadedAdvancedCPPFunctions proves
// that overloaded C++ declarations stay separately discoverable.
func TestIntegrationServiceFindSymbolReturnsOverloadedAdvancedCPPFunctions(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "advanced_cpp")
	service, ctx, _ := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "demo/operate", IncludeBody: true},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "advanced.cpp",
	})
	require.NoError(t, err)
	assert.Len(t, result.Symbols, 2)
}

// TestIntegrationServiceFindReferencingSymbolsReturnsCrossFileCaller proves that the references workflow can
// follow a declaration from the header into the C++ source that calls it.
func TestIntegrationServiceFindReferencingSymbolsReturnsCrossFileCaller(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "basic")
	service, ctx, _ := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "Greeter/greet"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "greeter.hpp",
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "call_greeter")
	require.True(t, ok, "expected call_greeter reference container, got %#v", result.Symbols)
	assert.Equal(t, "greeter.cpp", symbol.File)
	assert.Contains(t, symbol.Content, "greeter.greet()")
}

// TestIntegrationServiceFindReferencingSymbolsReturnsAdvancedCPPCallers proves
// that richer C++ member references are tracked across translation units.
func TestIntegrationServiceFindReferencingSymbolsReturnsAdvancedCPPCallers(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "advanced_cpp")
	service, ctx, _ := newIntegrationService(t)

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
			FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "demo/make_box"},
			WorkspaceRoot:                workspaceRoot,
			File:                         "advanced.cpp",
		})
		assert.NoError(collect, err)

		symbol, ok := findReferencingSymbol(result.Symbols, "use_widget")
		if !assert.Truef(collect, ok, "expected use_widget reference container, got %#v", result.Symbols) {
			return
		}

		assert.Equal(collect, "consumer.cpp", symbol.File)
		assert.Contains(collect, symbol.Content, "demo::make_box")
	}, 5*time.Second, 200*time.Millisecond)
}

// TestIntegrationGetSymbolsOverviewKeepsSameSourceTreeWithReferenceWorkflowFiles proves that opening the
// source-file reference workflow set does not change the normalized symbol tree for the same source file.
func TestIntegrationGetSymbolsOverviewKeepsSameSourceTreeWithReferenceWorkflowFiles(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "advanced_cpp")
	service, ctx, _ := newIntegrationService(t)

	baseline := mustMarshalOverviewSymbols(
		t,
		overviewSymbolsInWorkflow(t, ctx, service, workspaceRoot, "advanced.cpp", nil),
	)
	workflowFiles, err := helpers.CollectReferenceWorkflowFiles(
		workspaceRoot,
		"advanced.cpp",
		extensions,
		shouldIgnoreDir,
	)
	require.NoError(t, err)
	withWorkflow := mustMarshalOverviewSymbols(
		t,
		overviewSymbolsInWorkflow(t, ctx, service, workspaceRoot, "advanced.cpp", workflowFiles),
	)

	assert.Equal(t, baseline, withWorkflow)
}

// TestIntegrationGetSymbolsOverviewKeepsSameHeaderTreeWithWorkspaceWideWorkflowFiles proves that opening the
// workspace-wide header reference workflow does not change the normalized symbol tree for the same header file.
func TestIntegrationGetSymbolsOverviewKeepsSameHeaderTreeWithWorkspaceWideWorkflowFiles(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "advanced_cpp")
	service, ctx, _ := newIntegrationService(t)

	baseline := mustMarshalOverviewSymbols(
		t,
		overviewSymbolsInWorkflow(t, ctx, service, workspaceRoot, "advanced.hpp", nil),
	)
	workflowFiles, err := collectWorkspaceWideReferenceWorkflowFiles(
		workspaceRoot,
		"advanced.hpp",
		extensions,
		shouldIgnoreDir,
	)
	require.NoError(t, err)
	withWorkflow := mustMarshalOverviewSymbols(
		t,
		overviewSymbolsInWorkflow(t, ctx, service, workspaceRoot, "advanced.hpp", workflowFiles),
	)

	assert.Equal(t, baseline, withWorkflow)
}

// TestIntegrationRawDocumentSymbolsKeepSameSourcePayloadWithReferenceWorkflowFiles proves that opening the
// source-file reference workflow set does not change clangd's raw documentSymbol payload for the same source file.
func TestIntegrationRawDocumentSymbolsKeepSameSourcePayloadWithReferenceWorkflowFiles(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "advanced_cpp")
	service, ctx, _ := newIntegrationService(t)

	baseline := rawDocumentSymbolJSONInWorkflow(t, ctx, service, workspaceRoot, "advanced.cpp", nil)
	workflowFiles, err := helpers.CollectReferenceWorkflowFiles(
		workspaceRoot,
		"advanced.cpp",
		extensions,
		shouldIgnoreDir,
	)
	require.NoError(t, err)
	withWorkflow := rawDocumentSymbolJSONInWorkflow(t, ctx, service, workspaceRoot, "advanced.cpp", workflowFiles)

	assert.Equal(t, baseline, withWorkflow)
}

// TestIntegrationRawDocumentSymbolsKeepSameHeaderPayloadWithWorkspaceWideWorkflowFiles proves that opening the
// workspace-wide header reference workflow does not change clangd's raw documentSymbol payload for the same header file.
func TestIntegrationRawDocumentSymbolsKeepSameHeaderPayloadWithWorkspaceWideWorkflowFiles(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "advanced_cpp")
	service, ctx, _ := newIntegrationService(t)

	baseline := rawDocumentSymbolJSONInWorkflow(t, ctx, service, workspaceRoot, "advanced.hpp", nil)
	workflowFiles, err := collectWorkspaceWideReferenceWorkflowFiles(
		workspaceRoot,
		"advanced.hpp",
		extensions,
		shouldIgnoreDir,
	)
	require.NoError(t, err)
	withWorkflow := rawDocumentSymbolJSONInWorkflow(t, ctx, service, workspaceRoot, "advanced.hpp", workflowFiles)

	assert.Equal(t, baseline, withWorkflow)
}

// TestIntegrationGetSymbolsOverviewChangesWithCompileCommandsDefine proves that compile_commands content can change clangd symbol output.
func TestIntegrationGetSymbolsOverviewChangesWithCompileCommandsDefine(t *testing.T) {
	withDefineRoot := writeCompileCommandsExperimentWorkspace(t, true)
	withoutDefineRoot := writeCompileCommandsExperimentWorkspace(t, false)
	service, ctx, _ := newIntegrationService(t)

	withDefine, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 2},
		WorkspaceRoot:            withDefineRoot,
		File:                     "configurable.cpp",
	})
	require.NoError(t, err)
	assertOverviewContainsExactPath(t, withDefine.Symbols, "compile_commands_flagged")

	withoutDefine, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 2},
		WorkspaceRoot:            withoutDefineRoot,
		File:                     "configurable.cpp",
	})
	require.NoError(t, err)
	_, found := lsphelpers.FindOverviewSymbol(withoutDefine.Symbols, "compile_commands_flagged")
	assert.False(t, found)

	assert.NotEqual(
		t,
		mustMarshalOverviewSymbols(t, withDefine.Symbols),
		mustMarshalOverviewSymbols(t, withoutDefine.Symbols),
	)
}

// TestIntegrationGetSymbolsOverviewChangesWithCompileFlagsDefine proves that compile_flags content can change clangd symbol output.
func TestIntegrationGetSymbolsOverviewChangesWithCompileFlagsDefine(t *testing.T) {
	withDefineRoot := writeCompileFlagsExperimentWorkspace(t, true)
	withoutDefineRoot := writeCompileFlagsExperimentWorkspace(t, false)
	service, ctx, _ := newIntegrationService(t)

	withDefine, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 2},
		WorkspaceRoot:            withDefineRoot,
		File:                     "configurable.c",
	})
	require.NoError(t, err)
	assertOverviewContainsExactPath(t, withDefine.Symbols, "compile_flags_flagged")

	withoutDefine, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 2},
		WorkspaceRoot:            withoutDefineRoot,
		File:                     "configurable.c",
	})
	require.NoError(t, err)
	_, found := lsphelpers.FindOverviewSymbol(withoutDefine.Symbols, "compile_flags_flagged")
	assert.False(t, found)

	assert.NotEqual(
		t,
		mustMarshalOverviewSymbols(t, withDefine.Symbols),
		mustMarshalOverviewSymbols(t, withoutDefine.Symbols),
	)
}

// TestIntegrationServiceGetSymbolsOverviewReturnsAdvancedCSymbols proves that
// the richer C fixture exposes typedefs, enums, structs, unions, and function
// pointer declarations through clangd.
func TestIntegrationServiceGetSymbolsOverviewReturnsAdvancedCSymbols(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "advanced_c")
	service, ctx, _ := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 2},
		WorkspaceRoot:            workspaceRoot,
		File:                     "c_api.h",
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Symbols)

	assertOverviewContainsPathPrefix(t, result.Symbols, "Mode")
	assertOverviewContainsPathSuffix(t, result.Symbols, "/MODE_OFF")
	assertOverviewContainsPathSuffix(t, result.Symbols, "/MODE_ON")
	assertOverviewContainsPathPrefix(t, result.Symbols, "Stats")
	assertOverviewContainsPathSuffix(t, result.Symbols, "/count")
	assertOverviewContainsPathSuffix(t, result.Symbols, "/average")
	assertOverviewContainsExactPath(t, result.Symbols, "Number")
	assertOverviewContainsExactPath(t, result.Symbols, "transform_fn")
	assertOverviewContainsExactPath(t, result.Symbols, "Handler")
	assertOverviewContainsExactPath(t, result.Symbols, "Handler/transform")
	assertOverviewContainsExactPath(t, result.Symbols, "apply_mode")
	assertOverviewContainsExactPath(t, result.Symbols, "make_stats")
}

// TestIntegrationServiceSupportsMixedCAndCPPWorkspace proves that one clangd
// session can serve C and C++ files from the same workspace root.
func TestIntegrationServiceSupportsMixedCAndCPPWorkspace(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "mixed")
	service, ctx, _ := newIntegrationService(t)

	overviewResult, overviewErr := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 2},
		WorkspaceRoot:            workspaceRoot,
		File:                     "shared.h",
	})
	require.NoError(t, overviewErr)
	assertOverviewContainsExactPath(t, overviewResult.Symbols, "SharedPayload")
	assertOverviewContainsExactPath(t, overviewResult.Symbols, "SharedPayload/value")
	assertOverviewContainsExactPath(t, overviewResult.Symbols, "shared_c_function")

	findResult, findErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "mixed_cpp_entry",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "mixed_cpp.cpp",
	})
	require.NoError(t, findErr)

	symbol, ok := findFoundSymbol(findResult.Symbols, "mixed_cpp_entry")
	require.True(t, ok, "expected C++ function match, got %#v", findResult.Symbols)
	assert.Equal(t, "mixed_cpp.cpp", symbol.File)
	assert.Contains(t, symbol.Body, "widget.scale(payload.value)")

	referencesResult, referencesErr := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "shared_c_function"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "shared.h",
	})
	require.NoError(t, referencesErr)

	referencingSymbol, ok := findReferencingSymbol(referencesResult.Symbols, "mixed_cpp_entry")
	require.True(t, ok, "expected mixed_cpp_entry reference container, got %#v", referencesResult.Symbols)
	assert.Equal(t, "mixed_cpp.cpp", referencingSymbol.File)
	assert.Contains(t, referencingSymbol.Content, "shared_c_function(2)")
}

// TestIntegrationServiceWorksWithoutCompilationDatabase proves that clangd can
// still answer simple C and C++ queries when neither compile_commands.json nor
// compile_flags.txt is present.
func TestIntegrationServiceWorksWithoutCompilationDatabase(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "fallback")
	service, ctx, _ := newIntegrationService(t)

	cppOverview, cppErr := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 2},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fallback.cpp",
	})
	require.NoError(t, cppErr)
	assertOverviewContainsExactPath(t, cppOverview.Symbols, "FallbackWidget")
	assertOverviewContainsExactPath(t, cppOverview.Symbols, "FallbackWidget/double_value")
	assertOverviewContainsExactPath(t, cppOverview.Symbols, "fallback_cpp_entry")

	cResult, cErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "fallback_c_entry",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "fallback.c",
	})
	require.NoError(t, cErr)

	symbol, ok := findFoundSymbol(cResult.Symbols, "fallback_c_entry")
	require.True(t, ok, "expected C fallback symbol match, got %#v", cResult.Symbols)
	assert.Equal(t, "fallback.c", symbol.File)
	assert.Contains(t, symbol.Body, "return payload.value;")
}

// TestIntegrationServiceFindSymbolReturnsAdvancedCFunctionBody proves that the
// richer C fixture stays queryable through body lookup when compile_flags.txt is
// used instead of compile_commands.json.
func TestIntegrationServiceFindSymbolReturnsAdvancedCFunctionBody(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "advanced_c")
	service, ctx, _ := newIntegrationService(t)

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:        "/apply_mode",
			IncludeBody: true,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "c_api.c",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "apply_mode")
	require.True(t, ok, "expected C function match, got %#v", result.Symbols)
	assert.Contains(t, symbol.Body, "return clamp_to_zero(value);")

	cacheDir, cacheErr := service.cacheDir(workspaceRoot)
	require.NoError(t, cacheErr)
	_, statErr := os.Stat(filepath.Join(cacheDir, compileFlagsFileName))
	require.NoError(t, statErr)
}

// TestIntegrationServiceFindReferencingSymbolsReturnsAdvancedCCallers proves
// that richer C declarations resolve references in consumer code.
func TestIntegrationServiceFindReferencingSymbolsReturnsAdvancedCCallers(t *testing.T) {
	workspaceRoot := clangdFixtureRootByName(t, "advanced_c")
	service, ctx, _ := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "apply_mode"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "c_api.h",
	})
	require.NoError(t, err)

	symbol, ok := findReferencingSymbol(result.Symbols, "use_c_api")
	require.True(t, ok, "expected use_c_api reference container, got %#v", result.Symbols)
	assert.Equal(t, "consumer.c", symbol.File)
	assert.Contains(t, symbol.Content, "handler.transform = apply_mode")
}

// TestIntegrationServiceKeepsRootCachesSeparate proves that different workspace roots keep isolated runtime
// sessions and isolated managed clangd cache directories.
func TestIntegrationServiceKeepsRootCachesSeparate(t *testing.T) {
	firstRoot := clangdFixtureRootByName(t, "basic")
	secondRoot := clangdFixtureRootByName(t, "secondary")
	service, ctx, _ := newIntegrationService(t)

	firstResult, firstErr := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            firstRoot,
		File:                     "greeter.hpp",
	})
	require.NoError(t, firstErr)
	assert.NotEmpty(t, firstResult.Symbols)

	secondResult, secondErr := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            secondRoot,
		File:                     "utility.hpp",
	})
	require.NoError(t, secondErr)
	assert.NotEmpty(t, secondResult.Symbols)

	firstConn, firstConnErr := service.rt.EnsureConn(ctx, firstRoot)
	require.NoError(t, firstConnErr)
	secondConn, secondConnErr := service.rt.EnsureConn(ctx, secondRoot)
	require.NoError(t, secondConnErr)
	assert.NotSame(t, firstConn, secondConn)

	firstCacheDir, err := service.cacheDir(firstRoot)
	require.NoError(t, err)
	secondCacheDir, err := service.cacheDir(secondRoot)
	require.NoError(t, err)
	assert.NotEqual(t, firstCacheDir, secondCacheDir)

	_, firstCacheErr := os.Stat(filepath.Join(firstCacheDir, compileCommandsFileName))
	require.NoError(t, firstCacheErr)
	_, secondCacheErr := os.Stat(filepath.Join(secondCacheDir, compileFlagsFileName))
	require.NoError(t, secondCacheErr)
}

// TestIntegrationServiceFindSymbolUpdatesRenamedCPPFileWithoutRestart proves
// that the live clangd-backed session reacts to on-disk source and
// compile_commands changes inside one temp workspace.
func TestIntegrationServiceFindSymbolUpdatesRenamedCPPFileWithoutRestart(t *testing.T) {
	workspaceRoot := copyClangdFixtureRoot(t, "advanced_cpp")
	service, ctx, _ := newIntegrationService(t)

	_, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "advanced.hpp",
	})
	require.NoError(t, err)

	writeClangdWorkspaceFile(t, workspaceRoot, "watcher_probe.cpp", `int watcher_added_cpp_symbol() {
	return 1;
}
`)
	writeAdvancedCPPCompileCommands(t, workspaceRoot, true)

	require.Eventually(t, func() bool {
		result, findErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:        "watcher_added_cpp_symbol",
				IncludeBody: true,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "watcher_probe.cpp",
		})
		if findErr != nil {
			return false
		}

		symbol, ok := findFoundSymbol(result.Symbols, "watcher_added_cpp_symbol")

		return ok && strings.Contains(symbol.Body, `int watcher_added_cpp_symbol()`)
	}, clangdLiveWaitTimeout, clangdLiveWaitTick)

	writeClangdWorkspaceFile(t, workspaceRoot, "watcher_probe.cpp", `int watcher_renamed_cpp_symbol() {
	return 2;
}
`)

	require.Eventually(t, func() bool {
		oldResult, oldErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:        "watcher_added_cpp_symbol",
				IncludeBody: true,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "watcher_probe.cpp",
		})
		if oldErr != nil {
			return false
		}

		newResult, newErr := service.FindSymbol(ctx, &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:        "watcher_renamed_cpp_symbol",
				IncludeBody: true,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "watcher_probe.cpp",
		})
		if newErr != nil {
			return false
		}

		_, oldFound := findFoundSymbol(oldResult.Symbols, "watcher_added_cpp_symbol")
		newSymbol, newFound := findFoundSymbol(newResult.Symbols, "watcher_renamed_cpp_symbol")

		return !oldFound && newFound && strings.Contains(newSymbol.Body, `int watcher_renamed_cpp_symbol()`)
	}, clangdLiveWaitTimeout, clangdLiveWaitTick)
}

// TestIntegrationServiceGetSymbolsOverviewUpdatesRenamedCFileWithoutRestart
// proves that live overview responses follow on-disk C file rewrites inside one
// temp workspace backed by compile_flags.txt.
func TestIntegrationServiceGetSymbolsOverviewUpdatesRenamedCFileWithoutRestart(t *testing.T) {
	workspaceRoot := copyClangdFixtureRoot(t, "advanced_c")
	service, ctx, _ := newIntegrationService(t)

	_, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "c_api.h",
	})
	require.NoError(t, err)

	writeClangdWorkspaceFile(t, workspaceRoot, "watcher_probe.c", `int watcher_added_c_symbol(void) {
	return 1;
}
`)

	require.Eventually(t, func() bool {
		result, overviewErr := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
			WorkspaceRoot:            workspaceRoot,
			File:                     "watcher_probe.c",
		})
		if overviewErr != nil {
			return false
		}

		_, ok := lsphelpers.FindOverviewSymbol(result.Symbols, "watcher_added_c_symbol")

		return ok
	}, clangdLiveWaitTimeout, clangdLiveWaitTick)

	writeClangdWorkspaceFile(t, workspaceRoot, "watcher_probe.c", `int watcher_renamed_c_symbol(void) {
	return 2;
}
`)

	require.Eventually(t, func() bool {
		result, overviewErr := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
			WorkspaceRoot:            workspaceRoot,
			File:                     "watcher_probe.c",
		})
		if overviewErr != nil {
			return false
		}

		_, oldFound := lsphelpers.FindOverviewSymbol(result.Symbols, "watcher_added_c_symbol")
		_, newFound := lsphelpers.FindOverviewSymbol(result.Symbols, "watcher_renamed_c_symbol")

		return !oldFound && newFound
	}, clangdLiveWaitTimeout, clangdLiveWaitTick)
}

// newIntegrationService keeps repeated clangd service setup out of individual integration scenarios.
func newIntegrationService(t *testing.T) (*Service, context.Context, string) {
	t.Helper()

	requireClangdInstalled(t)
	cacheRoot := filepath.Join(t.TempDir(), "cache-root")
	service, err := New(cacheRoot)
	require.NoError(t, err)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, service.Close(ctx))
	})

	return service, ctx, cacheRoot
}

// clangdFixtureRootByName resolves one stable clangd fixture workspace by name.
func clangdFixtureRootByName(t *testing.T, fixtureName string) string {
	t.Helper()

	fixtureRoot, err := filepath.Abs(filepath.Join("testdata", fixtureName))
	require.NoError(t, err)

	return fixtureRoot
}

// copyClangdFixtureRoot copies one tracked clangd fixture tree into a temp
// workspace so live watcher tests can mutate files safely.
func copyClangdFixtureRoot(t *testing.T, fixtureName string) string {
	t.Helper()

	trackedRoot := clangdFixtureRootByName(t, fixtureName)
	workspaceRoot := filepath.Join(t.TempDir(), fixtureName)
	require.NotEqual(t, trackedRoot, workspaceRoot)
	require.NoError(t, copyClangdFixtureTree(trackedRoot, workspaceRoot))

	return workspaceRoot
}

// copyClangdFixtureTree recreates one tracked clangd fixture tree under a temp
// workspace with original permissions.
func copyClangdFixtureTree(sourceRoot string, destinationRoot string) error {
	return filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relativePath, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}

		if relativePath == "." {
			return os.MkdirAll(destinationRoot, clangdFixtureDirPermissions)
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

// writeClangdWorkspaceFile keeps live mutation tests focused on watcher
// behavior instead of repeated directory setup.
func writeClangdWorkspaceFile(t *testing.T, workspaceRoot, relativePath, content string) {
	t.Helper()

	absolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(relativePath))
	require.NoError(t, os.MkdirAll(filepath.Dir(absolutePath), clangdFixtureDirPermissions))
	require.NoError(t, os.WriteFile(absolutePath, []byte(content), clangdFixtureFilePermissions))
}

// writeAdvancedCPPCompileCommands rewrites the temp C++ fixture compile database
// so watcher tests can add probe translation units without restarting clangd.
func writeAdvancedCPPCompileCommands(t *testing.T, workspaceRoot string, includeWatcherProbe bool) {
	t.Helper()

	content := `[
  {
    "directory": ".",
    "file": "advanced.cpp",
    "command": "clang++ -std=c++20 -I. -c advanced.cpp"
  },
  {
    "directory": ".",
    "file": "consumer.cpp",
    "command": "clang++ -std=c++20 -I. -c consumer.cpp"
  },
  {
    "directory": ".",
    "file": "modules.cppm",
    "command": "clang++ -std=c++20 -I. -c modules.cppm"
  }`
	if includeWatcherProbe {
		content += `,
  {
    "directory": ".",
    "file": "watcher_probe.cpp",
    "command": "clang++ -std=c++20 -I. -c watcher_probe.cpp"
  }`
	}
	content += `
]
`

	writeClangdWorkspaceFile(t, workspaceRoot, compileCommandsFileName, content)
}

// writeMacroExternalCompileCommands materializes an external build directory so
// `.clangd` can point clangd at a valid compilation database outside the
// workspace root.
func writeMacroExternalCompileCommands(t *testing.T, workspaceRoot, buildRoot string) {
	t.Helper()

	require.NoError(t, os.MkdirAll(buildRoot, clangdFixtureDirPermissions))
	content := fmt.Sprintf(`[
  {
    "directory": %q,
    "file": "src/model.cpp",
    "command": "clang++ -std=c++20 -DMYLIB_LIBRARY -Iinclude -c src/model.cpp"
  },
  {
    "directory": %q,
    "file": "src/use.cpp",
    "command": "clang++ -std=c++20 -DMYLIB_LIBRARY -Iinclude -c src/use.cpp"
  }
]
`, filepath.ToSlash(workspaceRoot), filepath.ToSlash(workspaceRoot))
	require.NoError(t, os.WriteFile(
		filepath.Join(buildRoot, compileCommandsFileName),
		[]byte(content),
		clangdFixtureFilePermissions,
	))
}

// requireClangdInstalled fails fast with a clear message when the external clangd prerequisite is missing.
func requireClangdInstalled(t *testing.T) {
	t.Helper()

	_, err := exec.LookPath(clangdServerName)
	require.NoError(t, err, "clangd must be installed and available in PATH")
}

// assertOverviewContainsExactPath keeps integration assertions readable when document symbol output order changes.
func assertOverviewContainsExactPath(t *testing.T, symbols []domain.SymbolLocation, path string) {
	t.Helper()

	_, ok := lsphelpers.FindOverviewSymbol(symbols, path)
	require.Truef(t, ok, "expected %q in overview, got %#v", path, symbols)
}

// assertOverviewContainsPathPrefix keeps assertions stable when clangd adds
// duplicate-sibling suffixes like `@line:character` to the last segment.
func assertOverviewContainsPathPrefix(t *testing.T, symbols []domain.SymbolLocation, prefix string) {
	t.Helper()

	matches := collectOverviewPathsWithPrefix(symbols, prefix)
	require.NotEmptyf(t, matches, "expected prefix %q in overview, got %#v", prefix, symbols)
}

// assertOverviewContainsPathSuffix keeps assertions stable when clangd rewrites
// parent paths but still preserves the member suffix.
func assertOverviewContainsPathSuffix(t *testing.T, symbols []domain.SymbolLocation, suffix string) {
	t.Helper()

	for _, symbol := range symbols {
		if strings.HasSuffix(symbol.Path, suffix) {
			return
		}
	}

	require.Failf(t, "missing overview suffix", "expected suffix %q in overview, got %#v", suffix, symbols)
}

// findFoundSymbol keeps flat find_symbol assertions short and focused.
func findFoundSymbol(symbols []domain.FoundSymbol, path string) (domain.FoundSymbol, bool) {
	for _, symbol := range symbols {
		if symbol.Path == path {
			return symbol, true
		}
	}

	return domain.FoundSymbol{}, false
}

// findReferencingSymbol keeps grouped-reference assertions readable in integration tests.
func findReferencingSymbol(symbols []domain.ReferencingSymbol, path string) (domain.ReferencingSymbol, bool) {
	for _, symbol := range symbols {
		if symbol.Path == path {
			return symbol, true
		}
	}

	return domain.ReferencingSymbol{}, false
}

// collectOverviewPathsWithPrefix keeps overload assertions readable when clangd
// adds duplicate-path suffixes for same-name siblings.
func collectOverviewPathsWithPrefix(symbols []domain.SymbolLocation, prefix string) []string {
	paths := make([]string, 0)
	for _, symbol := range symbols {
		if strings.HasPrefix(symbol.Path, prefix) {
			paths = append(paths, symbol.Path)
		}
	}

	return paths
}

// overviewSymbolsInWorkflow reads one normalized clangd symbol tree, optionally inside an opened reference workflow context.
func overviewSymbolsInWorkflow(
	t *testing.T,
	ctx context.Context,
	service *Service,
	workspaceRoot string,
	relativePath string,
	workflowFiles []string,
) []domain.SymbolLocation {
	t.Helper()

	conn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)

	request := &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 6},
		WorkspaceRoot:            workspaceRoot,
		File:                     relativePath,
	}
	var result domain.GetSymbolsOverviewResult
	runOverview := func(callCtx context.Context) error {
		var callErr error
		result, callErr = service.std.GetSymbolsOverview(callCtx, request)

		return callErr
	}

	if len(workflowFiles) == 0 {
		require.NoError(t, runOverview(ctx))

		return result.Symbols
	}

	err = helpers.RunWithReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		workflowFiles,
		service.withRequestDocument,
		runOverview,
	)
	require.NoError(t, err)

	return result.Symbols
}

// mustMarshalOverviewSymbols turns one normalized symbol tree into stable JSON so integration experiments can diff it directly.
func mustMarshalOverviewSymbols(t *testing.T, symbols []domain.SymbolLocation) string {
	t.Helper()

	encoded, err := json.MarshalIndent(symbols, "", "  ")
	require.NoError(t, err)

	return string(encoded)
}

// rawDocumentSymbolJSONInWorkflow reads one live clangd documentSymbol payload, explicitly opening the target file inside the current workflow context.
func rawDocumentSymbolJSONInWorkflow(
	t *testing.T,
	ctx context.Context,
	service *Service,
	workspaceRoot string,
	relativePath string,
	workflowFiles []string,
) string {
	t.Helper()

	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	require.NoError(t, err)
	conn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)

	var payload []json.RawMessage
	requestSymbols := func(callCtx context.Context) error {
		params := &protocol.DocumentSymbolParams{
			WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
			PartialResultParams:    protocol.PartialResultParams{},
			TextDocument:           protocol.TextDocumentIdentifier{URI: uri.File(absolutePath)},
		}

		return protocol.Call(
			callCtx,
			conn,
			protocol.MethodTextDocumentDocumentSymbol,
			params,
			&payload,
		)
	}
	openTargetAndRequest := func(callCtx context.Context) error {
		return service.withRequestDocument(callCtx, conn, absolutePath, requestSymbols)
	}

	if len(workflowFiles) == 0 {
		require.NoError(t, openTargetAndRequest(ctx))

		return mustMarshalRawMessages(t, payload)
	}

	err = helpers.RunWithReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		workflowFilesWithoutTarget(workflowFiles, relativePath),
		service.withRequestDocument,
		openTargetAndRequest,
	)
	require.NoError(t, err)

	return mustMarshalRawMessages(t, payload)
}

// workflowFilesWithoutTarget avoids double-opening the target file when an experiment needs the target opened explicitly around the raw request.
func workflowFilesWithoutTarget(workflowFiles []string, targetRelativePath string) []string {
	filtered := make([]string, 0, len(workflowFiles))
	for _, relativePath := range workflowFiles {
		if relativePath != targetRelativePath {
			filtered = append(filtered, relativePath)
		}
	}

	return filtered
}

// mustMarshalRawMessages turns one raw LSP payload into stable JSON so integration experiments can diff it directly.
func mustMarshalRawMessages(t *testing.T, payload []json.RawMessage) string {
	t.Helper()

	encoded, err := json.MarshalIndent(payload, "", "  ")
	require.NoError(t, err)

	return string(encoded)
}

// writeCompileCommandsExperimentWorkspace builds one temp C++ workspace whose symbol surface depends on compile_commands defines.
func writeCompileCommandsExperimentWorkspace(t *testing.T, withDefine bool) string {
	t.Helper()

	workspaceRoot := filepath.Join(t.TempDir(), "compile-commands-experiment")
	defineFlag := ""
	if withDefine {
		defineFlag = " -DASTERIA_EXPERIMENT_FLAG"
	}

	writeClangdWorkspaceFile(t, workspaceRoot, "configurable.cpp", `int always_visible() {
	return 1;
}

#ifdef ASTERIA_EXPERIMENT_FLAG
int compile_commands_flagged() {
	return 2;
}
#endif
`)
	writeClangdWorkspaceFile(
		t,
		workspaceRoot,
		compileCommandsFileName,
		fmt.Sprintf(`[
  {
    "directory": %q,
    "file": "configurable.cpp",
    "command": "clang++ -std=c++20%s -c configurable.cpp"
  }
]
`, filepath.ToSlash(workspaceRoot), defineFlag),
	)

	return workspaceRoot
}

// writeCompileFlagsExperimentWorkspace builds one temp C workspace whose symbol surface depends on compile_flags defines.
func writeCompileFlagsExperimentWorkspace(t *testing.T, withDefine bool) string {
	t.Helper()

	workspaceRoot := filepath.Join(t.TempDir(), "compile-flags-experiment")
	flags := "-std=c11\n"
	if withDefine {
		flags += "-DASTERIA_EXPERIMENT_FLAG\n"
	}

	writeClangdWorkspaceFile(t, workspaceRoot, "configurable.c", `int always_visible(void) {
	return 1;
}

#ifdef ASTERIA_EXPERIMENT_FLAG
int compile_flags_flagged(void) {
	return 2;
}
#endif
`)
	writeClangdWorkspaceFile(t, workspaceRoot, compileFlagsFileName, flags)

	return workspaceRoot
}
