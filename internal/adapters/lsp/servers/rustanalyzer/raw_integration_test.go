//go:build integration_tests

package lsprustanalyzer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// TestIntegrationRawDocumentSymbolsForReexportsAndMacro proves that rust-analyzer exposes the exported macro
// in documentSymbol while omitting `pub use ... as ...` aliases from the same file's outline.
func TestIntegrationRawDocumentSymbolsForReexportsAndMacro(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	conn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)

	targetAbsolutePath := filepath.Join(workspaceRoot, "src", "lib.rs")
	err = service.withRequestDocument(ctx, conn, targetAbsolutePath, func(callCtx context.Context) error {
		rawSymbols := rawRustDocumentSymbols(t, callCtx, conn, &protocol.DocumentSymbolParams{
			WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
			PartialResultParams:    protocol.PartialResultParams{},
			TextDocument: protocol.TextDocumentIdentifier{
				URI: uri.File(targetAbsolutePath),
			},
		})

		names := rawDocumentSymbolNames(rawSymbols)
		assert.NotContains(t, names, "ReexportedBucket")
		assert.NotContains(t, names, "reexported_make_bucket")
		assert.Contains(t, names, "exported_bucket_macro")

		return nil
	})
	require.NoError(t, err)
}

// TestIntegrationRawReferencesForReexportedFunction proves that the raw reference request for the alias stays
// inside the known fixture files, even though rust-analyzer may return empty, same-file, or downstream results
// depending on platform and server version.
func TestIntegrationRawReferencesForReexportedFunction(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	actual := rawRustReferencesForTextScenario(
		t,
		ctx,
		service,
		workspaceRoot,
		filepath.ToSlash(filepath.Join("src", "lib.rs")),
		"reexported_make_bucket",
	)

	assertRawReferencesStayWithinFiles(t, actual,
		filepath.ToSlash(filepath.Join("src", "lib.rs")),
		filepath.ToSlash(filepath.Join("src", "references.rs")),
	)
}

// TestIntegrationRawReferencesForExportedMacro proves that the raw reference request for the exported macro
// stays inside the known fixture files, even though rust-analyzer may return empty, same-file, or downstream
// results depending on platform and server version.
func TestIntegrationRawReferencesForExportedMacro(t *testing.T) {
	workspaceRoot := rustFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	actual := rawRustReferencesForTextScenario(
		t,
		ctx,
		service,
		workspaceRoot,
		filepath.ToSlash(filepath.Join("src", "lib.rs")),
		"exported_bucket_macro",
	)

	assertRawReferencesStayWithinFiles(t, actual,
		filepath.ToSlash(filepath.Join("src", "lib.rs")),
		filepath.ToSlash(filepath.Join("src", "references.rs")),
	)
}

// rawRustReferencesForTextScenario resolves one target position from source text so raw-reference tests can
// prove server behavior for declarations that may or may not appear in documentSymbol.
func rawRustReferencesForTextScenario(
	t *testing.T,
	ctx context.Context,
	service *Service,
	workspaceRoot string,
	targetRelativePath string,
	targetText string,
) []rawReferenceOccurrence {
	t.Helper()

	conn, err := service.rt.EnsureConn(ctx, workspaceRoot)
	require.NoError(t, err)

	referenceWorkflowFiles, err := helpers.CollectReferenceWorkflowFiles(
		workspaceRoot,
		targetRelativePath,
		extensions,
		shouldIgnoreDir,
	)
	require.NoError(t, err)

	targetAbsolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(targetRelativePath))
	var occurrences []rawReferenceOccurrence
	err = helpers.RunWithReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		referenceWorkflowFiles,
		service.withRequestDocument,
		func(callCtx context.Context) error {
			position := rawSelectionStartForUniqueText(t, targetAbsolutePath, targetText)
			locations := rawRustReferences(t, callCtx, conn, &protocol.ReferenceParams{
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
		},
	)
	require.NoError(t, err)

	return occurrences
}

// rawSelectionStartForUniqueText resolves one target position directly from file text so raw tests can
// select declarations even when rust-analyzer omits them from documentSymbol.
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

// rawRustDocumentSymbols reads the live rust-analyzer documentSymbol payload as hierarchical symbols.
func rawRustDocumentSymbols(
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

// rawRustReferences reads the live textDocument/references response as protocol locations.
func rawRustReferences(
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

// summarizeRawReferenceLocations keeps raw-reference assertions stable across URI formatting and response ordering.
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

// rawReferenceOccurrence keeps raw-reference assertions focused on the live server evidence.
type rawReferenceOccurrence struct {
	File      string
	Line      uint32
	Character uint32
}

func assertRawReferencesStayWithinFiles(t *testing.T, occurrences []rawReferenceOccurrence, expectedFiles ...string) {
	t.Helper()

	allowedFiles := make(map[string]struct{}, len(expectedFiles))
	for _, expectedFile := range expectedFiles {
		allowedFiles[expectedFile] = struct{}{}
	}

	for _, occurrence := range occurrences {
		_, ok := allowedFiles[occurrence.File]
		assert.Truef(t, ok, "unexpected raw reference file %q in %#v", occurrence.File, occurrences)
	}
}
