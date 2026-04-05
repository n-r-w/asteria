//go:build integration_tests

package lsptsls

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

var rawReferenceScenarioFiles = []string{"fixture.js", "fixture.ts", "references.ts"}

var moduleReexportScenarioFiles = []string{"advanced.ts", "module_contracts.ts", "module_reexports.ts"}

// TestIntegrationRawReferencesOpenFileSetsForMakeBucket documents how each open-file set affects raw
// cross-file function references for makeBucket in the live TypeScript server.
func TestIntegrationRawReferencesOpenFileSetsForMakeBucket(t *testing.T) {
	// These raw-server checks intentionally run serially. They exercise live tsls
	// indexing immediately after a burst of didOpen notifications, and parallel
	// execution makes the observed wire-level behavior flaky without changing the
	// production adapter logic we want to document.

	workspaceRoot := copyTSLSFixtureRoot(t, "basic")

	scenarios := []rawReferenceOpenFileScenario{
		{
			Name:          "target file only",
			RelativeFiles: []string{"fixture.ts"},
			Expected:      []rawReferenceOccurrence{},
		},
		{
			Name:          "scenario file set with reference participants",
			RelativeFiles: rawReferenceScenarioFiles,
			Expected: []rawReferenceOccurrence{
				{File: "references.ts", Line: 0, Character: 39},
				{File: "references.ts", Line: 4, Character: 18},
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			service, ctx := newIntegrationService(t)
			requireEventuallyRawReferencesForScenario(
				t,
				ctx,
				service,
				workspaceRoot,
				"fixture.ts",
				"makeBucket",
				scenario.RelativeFiles,
				scenario.Expected,
			)
		})
	}
}

// TestIntegrationRawReferencesOpenFileSetsForFixtureBucket documents how each open-file set affects raw
// class references for FixtureBucket in the live TypeScript server.
func TestIntegrationRawReferencesOpenFileSetsForFixtureBucket(t *testing.T) {
	workspaceRoot := copyTSLSFixtureRoot(t, "basic")

	scenarios := []rawReferenceOpenFileScenario{
		{
			Name:          "target file only",
			RelativeFiles: []string{"fixture.ts"},
			Expected: []rawReferenceOccurrence{
				{File: "fixture.ts", Line: 29, Character: 43},
				{File: "fixture.ts", Line: 30, Character: 15},
			},
		},
		{
			Name:          "scenario file set with reference participants",
			RelativeFiles: rawReferenceScenarioFiles,
			Expected: []rawReferenceOccurrence{
				{File: "fixture.ts", Line: 29, Character: 43},
				{File: "fixture.ts", Line: 30, Character: 15},
				{File: "references.ts", Line: 0, Character: 24},
				{File: "references.ts", Line: 3, Character: 21},
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			service, ctx := newIntegrationService(t)
			requireEventuallyRawReferencesForScenario(
				t,
				ctx,
				service,
				workspaceRoot,
				"fixture.ts",
				"FixtureBucket",
				scenario.RelativeFiles,
				scenario.Expected,
			)
		})
	}
}

// TestIntegrationRawDocumentSymbolsForModuleReexports proves the live tsls documentSymbol payload for
// module_reexports.ts before any shared-service fallback changes rely on it.
func TestIntegrationRawDocumentSymbolsForModuleReexports(t *testing.T) {
	workspaceRoot := copyTSLSFixtureRoot(t, "basic")
	service, ctx := newIntegrationService(t)

	conn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)

	targetAbsolutePath := filepath.Join(workspaceRoot, "module_reexports.ts")
	err = helpers.WithRequestDocument(languageIDForExtension)(ctx, conn, targetAbsolutePath, func(callCtx context.Context) error {
		rawSymbols := rawTSLSDocumentSymbolPayload(t, callCtx, conn, &protocol.DocumentSymbolParams{
			WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
			PartialResultParams:    protocol.PartialResultParams{},
			TextDocument: protocol.TextDocumentIdentifier{
				URI: uri.File(targetAbsolutePath),
			},
		})
		assert.Empty(t, rawSymbols)

		return nil
	})
	require.NoError(t, err)
}

// TestIntegrationRawReferencesForModuleReexportTargetsResolveSourceReferences proves that once tsls finishes
// alias-resolution work for the open project, raw references for module re-export declarations point at the
// source usages in `advanced.ts`.
func TestIntegrationRawReferencesForModuleReexportTargetsResolveSourceReferences(t *testing.T) {
	workspaceRoot := copyTSLSFixtureRoot(t, "basic")
	scenarios := []struct {
		name       string
		targetText string
		expected   []rawReferenceOccurrence
	}{
		{
			name:       "default export alias",
			targetText: "defaultBucket",
			expected: []rawReferenceOccurrence{
				{File: "advanced.ts", Line: 1, Character: 9},
				{File: "advanced.ts", Line: 1, Character: 26},
				{File: "advanced.ts", Line: 30, Character: 35},
			},
		},
		{
			name:       "named export alias",
			targetText: "aliasBucket",
			expected: []rawReferenceOccurrence{
				{File: "advanced.ts", Line: 50, Character: 54},
			},
		},
		{
			name:       "type re-export alias",
			targetText: "ReexportedShape",
			expected: []rawReferenceOccurrence{
				{File: "advanced.ts", Line: 2, Character: 14},
				{File: "advanced.ts", Line: 15, Character: 51},
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			service, ctx := newIntegrationService(t)
			requireEventuallyRawReferencesForTextScenario(
				t,
				ctx,
				service,
				workspaceRoot,
				"module_reexports.ts",
				scenario.targetText,
				moduleReexportScenarioFiles,
					scenario.expected,
			)
		})
	}
}

func requireEventuallyRawReferencesForScenario(
	t *testing.T,
	ctx context.Context,
	service *Service,
	workspaceRoot string,
	targetRelativePath string,
	targetSymbolName string,
	relativeFiles []string,
	expected []rawReferenceOccurrence,
) {
	t.Helper()

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		actual := rawReferencesForScenario(
			t,
			ctx,
			service,
			workspaceRoot,
			targetRelativePath,
			targetSymbolName,
			relativeFiles,
		)
		assert.Equal(collect, expected, actual)
	}, 5*time.Second, 100*time.Millisecond)
}

func requireEventuallyRawReferencesForTextScenario(
	t *testing.T,
	ctx context.Context,
	service *Service,
	workspaceRoot string,
	targetRelativePath string,
	targetText string,
	relativeFiles []string,
	expected []rawReferenceOccurrence,
) {
	t.Helper()

	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		actual := rawReferencesForTextScenario(
			t,
			ctx,
			service,
			workspaceRoot,
			targetRelativePath,
			targetText,
			relativeFiles,
		)
		assert.Equal(collect, expected, actual)
	}, 5*time.Second, 100*time.Millisecond)
}

// rawReferenceOpenFileScenario keeps the compared open-file sets readable in table-driven raw-reference tests.
type rawReferenceOpenFileScenario struct {
	Name          string
	RelativeFiles []string
	Expected      []rawReferenceOccurrence
}

// rawReferenceOccurrence keeps raw reference assertions focused on the live server evidence.
type rawReferenceOccurrence struct {
	File      string
	Line      uint32
	Character uint32
}

// rawReferencesForScenario derives the target position from raw document symbols and then requests raw
// references while the whole scenario file set stays open in one live tsls session.
func rawReferencesForScenario(
	t *testing.T,
	ctx context.Context,
	service *Service,
	workspaceRoot string,
	targetRelativePath string,
	targetSymbolName string,
	relativeFiles []string,
) []rawReferenceOccurrence {
	t.Helper()

	conn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)

	targetAbsolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(targetRelativePath))
	openFiles := discoveryOpenFileSet(targetRelativePath, relativeFiles)
	absoluteOpenFiles := make([]string, 0, len(openFiles))
	for _, relativePath := range openFiles {
		absoluteOpenFiles = append(absoluteOpenFiles, filepath.Join(workspaceRoot, filepath.FromSlash(relativePath)))
	}

	var occurrences []rawReferenceOccurrence
	err = runWithOpenFiles(t, ctx, conn, absoluteOpenFiles, func(callCtx context.Context) error {
		if err := warmRequestDocument(callCtx, conn, targetAbsolutePath); err != nil {
			return err
		}

		position := rawSelectionStartForTopLevelSymbol(t, callCtx, conn, targetAbsolutePath, targetSymbolName)
		locations := rawTSLSReferences(t, callCtx, conn, &protocol.ReferenceParams{
			TextDocumentPositionParams: protocol.TextDocumentPositionParams{
				TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(targetAbsolutePath)},
				Position:     position,
			},
			WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
			PartialResultParams:    protocol.PartialResultParams{},
			Context:                protocol.ReferenceContext{IncludeDeclaration: false},
		})
		occurrences = summarizeRawReferenceLocations(t, workspaceRoot, locations)

		return nil
	})
	require.NoError(t, err)

	return occurrences
}

// rawReferencesForTextScenario derives the target position from source text so raw-reference tests can
// prove server behavior even when documentSymbol returns no declaration for the target file.
func rawReferencesForTextScenario(
	t *testing.T,
	ctx context.Context,
	service *Service,
	workspaceRoot string,
	targetRelativePath string,
	targetText string,
	relativeFiles []string,
) []rawReferenceOccurrence {
	t.Helper()

	conn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)

	targetAbsolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(targetRelativePath))
	openFiles := discoveryOpenFileSet(targetRelativePath, relativeFiles)
	absoluteOpenFiles := make([]string, 0, len(openFiles))
	for _, relativePath := range openFiles {
		absoluteOpenFiles = append(absoluteOpenFiles, filepath.Join(workspaceRoot, filepath.FromSlash(relativePath)))
	}

	var occurrences []rawReferenceOccurrence
	err = runWithOpenFiles(t, ctx, conn, absoluteOpenFiles, func(callCtx context.Context) error {
		position := rawSelectionStartForUniqueText(t, targetAbsolutePath, targetText)
		locations := rawTSLSReferences(t, callCtx, conn, &protocol.ReferenceParams{
			TextDocumentPositionParams: protocol.TextDocumentPositionParams{
				TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(targetAbsolutePath)},
				Position:     position,
			},
			WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
			PartialResultParams:    protocol.PartialResultParams{},
			Context:                protocol.ReferenceContext{IncludeDeclaration: false},
		})
		occurrences = summarizeRawReferenceLocations(t, workspaceRoot, locations)

		return nil
	})
	require.NoError(t, err)

	return occurrences
}

// discoveryOpenFileSet keeps the target file open and adds the remaining scenario files in stable order.
func discoveryOpenFileSet(targetRelativePath string, relativeFiles []string) []string {
	remainingFiles := make([]string, 0, len(relativeFiles))
	for _, relativePath := range relativeFiles {
		if relativePath == targetRelativePath {
			continue
		}
		remainingFiles = append(remainingFiles, relativePath)
	}
	sort.Strings(remainingFiles)

	return append(remainingFiles, targetRelativePath)
}

// runWithOpenFiles nests the shared request-document helper so one scenario can keep several files open.
func runWithOpenFiles(
	t *testing.T,
	ctx context.Context,
	conn jsonrpc2.Conn,
	absolutePaths []string,
	run func(context.Context) error,
) error {
	t.Helper()

	return runWithOpenReferenceWorkflowFiles(ctx, conn, absolutePaths, newWithRequestDocument(), func(callCtx context.Context) error {
		if err := warmRequestDocuments(callCtx, conn, absolutePaths); err != nil {
			return err
		}

		return run(callCtx)
	})
}

// rawSelectionStartForTopLevelSymbol reads live document symbols and returns the selection start for one target.
func rawSelectionStartForTopLevelSymbol(
	t *testing.T,
	ctx context.Context,
	conn jsonrpc2.Conn,
	targetAbsolutePath string,
	targetSymbolName string,
) protocol.Position {
	t.Helper()

	symbols := rawTSLSDocumentSymbols(t, ctx, conn, &protocol.DocumentSymbolParams{
		WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
		PartialResultParams:    protocol.PartialResultParams{},
		TextDocument: protocol.TextDocumentIdentifier{
			URI: uri.File(targetAbsolutePath),
		},
	})

	targetSymbol, ok := findRawDocumentSymbol(symbols, targetSymbolName)
	require.Truef(t, ok, "expected raw symbol %q, got %#v", targetSymbolName, rawDocumentSymbolNames(symbols))

	return targetSymbol.SelectionRange.Start
}

// rawSelectionStartForUniqueText resolves one target position directly from file text so raw tests can
// select declarations that the live server omits from documentSymbol.
func rawSelectionStartForUniqueText(t *testing.T, absolutePath string, targetText string) protocol.Position {
	t.Helper()

	rawContent, err := os.ReadFile(filepath.Clean(absolutePath))
	require.NoError(t, err)

	content := string(rawContent)
	matchOffset := strings.Index(content, targetText)
	require.NotEqualf(t, -1, matchOffset, "expected to find %q in %s", targetText, absolutePath)
	require.Equal(t, matchOffset, strings.LastIndex(content, targetText), "expected unique target text %q", targetText)

	prefix := content[:matchOffset]
	line := uint32(strings.Count(prefix, "\n"))
	lastNewlineOffset := strings.LastIndex(prefix, "\n")
	character := uint32(matchOffset)
	if lastNewlineOffset >= 0 {
		character = uint32(matchOffset - lastNewlineOffset - 1)
	}

	return protocol.Position{Line: line, Character: character}
}

// rawTSLSDocumentSymbolPayload reads the live documentSymbol payload without assuming whether the server
// returns document symbols, symbol information, or an empty result.
func rawTSLSDocumentSymbolPayload(
	t *testing.T,
	ctx context.Context,
	conn jsonrpc2.Conn,
	params *protocol.DocumentSymbolParams,
) []json.RawMessage {
	t.Helper()

	var rawSymbols []json.RawMessage
	err := protocol.Call(ctx, conn, protocol.MethodTextDocumentDocumentSymbol, params, &rawSymbols)
	require.NoError(t, err)

	return rawSymbols
}

// rawTSLSDocumentSymbols reads live documentSymbol payloads as hierarchical DocumentSymbol items.
func rawTSLSDocumentSymbols(
	t *testing.T,
	ctx context.Context,
	conn jsonrpc2.Conn,
	params *protocol.DocumentSymbolParams,
) []protocol.DocumentSymbol {
	t.Helper()

	rawSymbols := rawTSLSDocumentSymbolPayload(t, ctx, conn, params)
	require.NotEmpty(t, rawSymbols)

	symbols := make([]protocol.DocumentSymbol, 0, len(rawSymbols))
	for _, rawSymbol := range rawSymbols {
		fields := make(map[string]json.RawMessage)
		err := json.Unmarshal(rawSymbol, &fields)
		require.NoError(t, err)
		require.NotContains(t, fields, "location")

		var symbol protocol.DocumentSymbol
		err = json.Unmarshal(rawSymbol, &symbol)
		require.NoError(t, err)
		symbols = append(symbols, symbol)
	}

	return symbols
}

// rawTSLSReferences reads the live textDocument/references response as protocol locations.
func rawTSLSReferences(
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

// summarizeRawReferenceLocations keeps assertions stable across URI formatting and response ordering.
func summarizeRawReferenceLocations(
	t *testing.T,
	workspaceRoot string,
	locations []protocol.Location,
) []rawReferenceOccurrence {
	t.Helper()

	occurrences := make([]rawReferenceOccurrence, 0, len(locations))
	for _, location := range locations {
		relativePath, err := filepath.Rel(workspaceRoot, location.URI.Filename())
		require.NoError(t, err)
		occurrences = append(occurrences, rawReferenceOccurrence{
			File:      filepath.ToSlash(relativePath),
			Line:      location.Range.Start.Line,
			Character: location.Range.Start.Character,
		})
	}

	slices.SortFunc(occurrences, func(left, right rawReferenceOccurrence) int {
		if byFile := strings.Compare(left.File, right.File); byFile != 0 {
			return byFile
		}
		if left.Line != right.Line {
			if left.Line < right.Line {
				return -1
			}

			return 1
		}
		if left.Character != right.Character {
			if left.Character < right.Character {
				return -1
			}

			return 1
		}

		return 0
	})

	return occurrences
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
func findRawDocumentSymbol(symbols []protocol.DocumentSymbol, name string) (protocol.DocumentSymbol, bool) {
	for _, symbol := range symbols {
		if symbol.Name == name {
			return symbol, true
		}
	}

	return protocol.DocumentSymbol{}, false
}
