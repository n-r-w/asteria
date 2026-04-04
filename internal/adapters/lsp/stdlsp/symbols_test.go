package stdlsp

import (
	"testing"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.lsp.dev/protocol"
)

// TestNamePathMatcherMatchesPatterns proves that the matcher follows the supported simple, suffix,
// absolute, and substring rules for the last path segment.
func TestNamePathMatcherMatchesPatterns(t *testing.T) {
	t.Parallel()

	matcher := newNamePathMatcher("FixtureBucket/Describe", false)
	assert.True(t, matcher.matches("FixtureBucket/Describe"))
	assert.True(t, matcher.matches("pkg/FixtureBucket/Describe"))
	assert.True(t, matcher.matches("FixtureBucket/Describe@19:4"))
	assert.False(t, matcher.matches("FixtureBucket/MeasureDepth"))

	absMatcher := newNamePathMatcher("/FixtureBucket/Describe", false)
	assert.True(t, absMatcher.matches("FixtureBucket/Describe"))
	assert.True(t, absMatcher.matches("FixtureBucket/Describe@19:4"))
	assert.False(t, absMatcher.matches("pkg/FixtureBucket/Describe"))

	disambiguatedAbsMatcher := newNamePathMatcher("/FixtureBucket/Describe@19:4", false)
	assert.True(t, disambiguatedAbsMatcher.matches("FixtureBucket/Describe@19:4"))
	assert.False(t, disambiguatedAbsMatcher.matches("FixtureBucket/Describe@20:4"))

	substringMatcher := newNamePathMatcher("FixtureBucket/Desc", true)
	assert.True(t, substringMatcher.matches("FixtureBucket/Describe"))
	assert.True(t, substringMatcher.matches("FixtureBucket/Describe@19:4"))
	assert.False(t, substringMatcher.matches("FixtureBucket/MeasureDepth"))
}

// TestFindContainingNodePrefersInnermost proves that reference resolution maps a position
// to the closest enclosing symbol instead of a broader parent container.
func TestFindContainingNodePrefersInnermost(t *testing.T) {
	t.Parallel()

	root := &node{
		Kind:         int(protocol.SymbolKindFunction),
		NamePath:     "UseMakeBucketTwice",
		RelativePath: "references.go",
		Range: protocol.Range{
			Start: protocol.Position{Line: 1, Character: 0},
			End:   protocol.Position{Line: 8, Character: 1},
		},
		SelectionRange: protocol.Range{
			Start: protocol.Position{Line: 1, Character: 5},
			End:   protocol.Position{Line: 1, Character: 23},
		},
		Children: []*node{{
			Kind:         int(protocol.SymbolKindVariable),
			NamePath:     "UseMakeBucketTwice/right",
			RelativePath: "references.go",
			Range: protocol.Range{
				Start: protocol.Position{Line: 3, Character: 1},
				End:   protocol.Position{Line: 3, Character: 33},
			},
			SelectionRange: protocol.Range{
				Start: protocol.Position{Line: 3, Character: 1},
				End:   protocol.Position{Line: 3, Character: 6},
			},
		}},
	}

	match, ok := findContainingNode([]*node{root}, protocol.Position{Line: 3, Character: 20})
	require.True(t, ok)
	assert.Equal(t, "UseMakeBucketTwice/right", match.NamePath)
}

// TestMapRawSymbolsToOverviewRespectsDepth proves that overview mapping keeps top-level symbols
// and only includes descendants up to the requested depth.
func TestMapRawSymbolsToOverviewRespectsDepth(t *testing.T) {
	t.Parallel()

	rawSymbols := mustRawMessages(t, []protocol.DocumentSymbol{{
		Name:       "Service",
		Detail:     "",
		Kind:       protocol.SymbolKindStruct,
		Tags:       nil,
		Deprecated: false,
		Range: protocol.Range{
			Start: protocol.Position{Line: 0, Character: 0},
			End:   protocol.Position{Line: 20, Character: 0},
		},
		SelectionRange: protocol.Range{
			Start: protocol.Position{Line: 1, Character: 5},
			End:   protocol.Position{Line: 1, Character: 12},
		},
		Children: []protocol.DocumentSymbol{{
			Name:       "GetSymbolsOverview",
			Detail:     "",
			Kind:       protocol.SymbolKindMethod,
			Tags:       nil,
			Deprecated: false,
			Range: protocol.Range{
				Start: protocol.Position{Line: 4, Character: 0},
				End:   protocol.Position{Line: 10, Character: 0},
			},
			SelectionRange: protocol.Range{
				Start: protocol.Position{Line: 4, Character: 5},
				End:   protocol.Position{Line: 4, Character: 23},
			},
			Children: []protocol.DocumentSymbol{{
				Name:       "request",
				Detail:     "",
				Kind:       protocol.SymbolKindVariable,
				Tags:       nil,
				Deprecated: false,
				Range: protocol.Range{
					Start: protocol.Position{Line: 6, Character: 0},
					End:   protocol.Position{Line: 6, Character: 7},
				},
				SelectionRange: protocol.Range{
					Start: protocol.Position{Line: 6, Character: 1},
					End:   protocol.Position{Line: 6, Character: 8},
				},
				Children: nil,
			}},
		}},
	}})

	depthZero, err := mapRawSymbolsToOverview("fixture.go", 0, rawSymbols, JoinNamePath)
	require.NoError(t, err)
	depthOne, err := mapRawSymbolsToOverview("fixture.go", 1, rawSymbols, JoinNamePath)
	require.NoError(t, err)

	service, ok := helpers.FindOverviewSymbol(depthZero.Symbols, "Service")
	require.True(t, ok)
	assert.Equal(t, 0, service.StartLine)
	assert.Equal(t, 19, service.EndLine)
	assert.Equal(t, "fixture.go", service.File)
	_, ok = helpers.FindOverviewSymbol(depthZero.Symbols, "Service/GetSymbolsOverview")
	assert.False(t, ok)

	method, ok := helpers.FindOverviewSymbol(depthOne.Symbols, "Service/GetSymbolsOverview")
	require.True(t, ok)
	assert.Equal(t, 4, method.StartLine)
	assert.Equal(t, 9, method.EndLine)
	assert.Equal(t, int(protocol.SymbolKindMethod), method.Kind)
	_, ok = helpers.FindOverviewSymbol(depthOne.Symbols, "Service/GetSymbolsOverview/request")
	assert.False(t, ok)
}

// TestMapRawSymbolsToOverviewBuildsBestEffortNamePath proves that flat symbol responses still
// produce usable overview entries when the server does not return a hierarchy.
func TestMapRawSymbolsToOverviewBuildsBestEffortNamePath(t *testing.T) {
	t.Parallel()

	rawSymbols := mustRawMessages(t, []protocol.SymbolInformation{{
		Name:       "GetSymbolsOverview",
		Kind:       protocol.SymbolKindMethod,
		Tags:       nil,
		Deprecated: false,
		Location: protocol.Location{
			URI: "",
			Range: protocol.Range{
				Start: protocol.Position{Line: 8, Character: 2},
				End:   protocol.Position{Line: 12, Character: 0},
			},
		},
		ContainerName: "Service",
	}})

	result, err := mapRawSymbolsToOverview("fixture.go", 0, rawSymbols, JoinNamePath)
	require.NoError(t, err)

	location, ok := helpers.FindOverviewSymbol(result.Symbols, "Service/GetSymbolsOverview")
	require.True(t, ok)
	assert.Equal(t, 8, location.StartLine)
	assert.Equal(t, 11, location.EndLine)
	assert.Equal(t, "fixture.go", location.File)
	assert.Equal(t, int(protocol.SymbolKindMethod), location.Kind)
}

// TestMapRawSymbolsToTreeBuildsBestEffortNode proves that flat symbol responses still produce
// addressable tree nodes when the server does not return a hierarchy.
func TestMapRawSymbolsToTreeBuildsBestEffortNode(t *testing.T) {
	t.Parallel()

	rawSymbols := mustRawMessages(t, []protocol.SymbolInformation{{
		Name:       "GetSymbolsOverview",
		Kind:       protocol.SymbolKindMethod,
		Tags:       nil,
		Deprecated: false,
		Location: protocol.Location{
			URI: "",
			Range: protocol.Range{
				Start: protocol.Position{Line: 8, Character: 2},
				End:   protocol.Position{Line: 12, Character: 0},
			},
		},
		ContainerName: "Service",
	}})

	nodes, err := mapRawSymbolsToTree("fixture.go", rawSymbols, JoinNamePath)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "Service/GetSymbolsOverview", nodes[0].NamePath)
	assert.Equal(t, "fixture.go", nodes[0].RelativePath)
	assert.Equal(t, uint32(8), nodes[0].SelectionRange.Start.Line)
}

// TestMapRawSymbolsToTreeDisambiguatesDuplicateSiblingPaths proves that duplicate siblings become
// uniquely addressable in the exported path contract instead of colliding on the same final segment.
func TestMapRawSymbolsToTreeDisambiguatesDuplicateSiblingPaths(t *testing.T) {
	t.Parallel()

	rawSymbols := mustRawMessages(t, []protocol.DocumentSymbol{{
		Name:       "execute",
		Detail:     "",
		Kind:       protocol.SymbolKindFunction,
		Tags:       nil,
		Deprecated: false,
		Range: protocol.Range{
			Start: protocol.Position{Line: 0, Character: 0},
			End:   protocol.Position{Line: 11, Character: 1},
		},
		SelectionRange: protocol.Range{
			Start: protocol.Position{Line: 0, Character: 16},
			End:   protocol.Position{Line: 0, Character: 23},
		},
		Children: []protocol.DocumentSymbol{
			{
				Name:       "notification",
				Detail:     "",
				Kind:       protocol.SymbolKindVariable,
				Tags:       nil,
				Deprecated: false,
				Range: protocol.Range{
					Start: protocol.Position{Line: 2, Character: 8},
					End:   protocol.Position{Line: 3, Character: 32},
				},
				SelectionRange: protocol.Range{
					Start: protocol.Position{Line: 2, Character: 14},
					End:   protocol.Position{Line: 2, Character: 26},
				},
				Children: nil,
			},
			{
				Name:       "notification",
				Detail:     "",
				Kind:       protocol.SymbolKindVariable,
				Tags:       nil,
				Deprecated: false,
				Range: protocol.Range{
					Start: protocol.Position{Line: 6, Character: 4},
					End:   protocol.Position{Line: 7, Character: 28},
				},
				SelectionRange: protocol.Range{
					Start: protocol.Position{Line: 6, Character: 10},
					End:   protocol.Position{Line: 6, Character: 22},
				},
				Children: nil,
			},
		},
	}})

	nodes, err := mapRawSymbolsToTree("fixture.ts", rawSymbols, JoinNamePath)
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Len(t, nodes[0].Children, 2)
	assert.Equal(t, "execute/notification@2:14", nodes[0].Children[0].NamePath)
	assert.Equal(t, "execute/notification@6:10", nodes[0].Children[1].NamePath)
}

// TestCollectMatchedNodesFlattensRequestedDepth proves that shared traversal returns the match
// and only the requested descendant depth, while keeping duplicates out.
func TestCollectMatchedNodesFlattensRequestedDepth(t *testing.T) {
	t.Parallel()

	nodes := []*node{{
		Kind:         int(protocol.SymbolKindStruct),
		NamePath:     "Service",
		RelativePath: "fixture.go",
		SelectionRange: protocol.Range{
			Start: protocol.Position{Line: 1, Character: 5},
			End:   protocol.Position{Line: 1, Character: 12},
		},
		Children: []*node{{
			Kind:         int(protocol.SymbolKindMethod),
			NamePath:     "Service/GetSymbolsOverview",
			RelativePath: "fixture.go",
			SelectionRange: protocol.Range{
				Start: protocol.Position{Line: 4, Character: 5},
				End:   protocol.Position{Line: 4, Character: 23},
			},
			Children: []*node{{
				Kind:         int(protocol.SymbolKindVariable),
				NamePath:     "Service/GetSymbolsOverview/request",
				RelativePath: "fixture.go",
				SelectionRange: protocol.Range{
					Start: protocol.Position{Line: 6, Character: 1},
					End:   protocol.Position{Line: 6, Character: 8},
				},
			}},
		}},
	}}

	matched := collectMatchedNodesForRequest(
		nodes,
		newNamePathMatcher("Service", false),
		1,
		nil,
		nil,
	)

	require.Len(t, matched, 2)
	assert.Equal(t, "Service", matched[0].NamePath)
	assert.Equal(t, "Service/GetSymbolsOverview", matched[1].NamePath)
}

// TestFindUniqueNodeSuggestsCandidates makes ambiguity errors actionable for the next query.
func TestFindUniqueNodeSuggestsCandidates(t *testing.T) {
	t.Parallel()

	_, err := findUniqueNode([]*node{
		{
			Kind:         int(protocol.SymbolKindMethod),
			NamePath:     "Service/Register",
			RelativePath: "fixture.go",
			SelectionRange: protocol.Range{
				Start: protocol.Position{Line: 4, Character: 1},
				End:   protocol.Position{Line: 4, Character: 9},
			},
		},
		{
			Kind:         int(protocol.SymbolKindMethod),
			NamePath:     "Server/Register",
			RelativePath: "fixture.go",
			SelectionRange: protocol.Range{
				Start: protocol.Position{Line: 14, Character: 1},
				End:   protocol.Position{Line: 14, Character: 9},
			},
		},
	}, "Register")
	require.Error(t, err)
	require.ErrorContains(t, err, `multiple symbols match "Register"`)
	require.ErrorContains(t, err, `use a more specific symbol_path`)
	require.ErrorContains(t, err, `Candidates:`)
	require.ErrorContains(t, err, `Service/Register (fixture.go:4)`)
	require.ErrorContains(t, err, `Server/Register (fixture.go:14)`)
}

// TestGroupReferenceMatchesKeepsOneRepresentativeReference proves that grouped references keep one
// deterministic representative reference snippet for each referencing symbol.
func TestGroupReferenceMatchesKeepsOneRepresentativeReference(t *testing.T) {
	t.Parallel()

	matches := []referenceMatch{
		{
			Container: domain.SymbolLocation{Kind: int(protocol.SymbolKindFunction), Path: "UseMakeBucketTwice", File: "references.go", StartLine: 2, EndLine: 6},
			Evidence:  referenceEvidenceCandidate{StartLine: 3, EndLine: 3, ContentStartLine: 3, ContentEndLine: 3, Column: 10, Content: "3: MakeBucket()"},
		},
		{
			Container: domain.SymbolLocation{Kind: int(protocol.SymbolKindFunction), Path: "UseMakeBucketTwice", File: "references.go", StartLine: 2, EndLine: 6},
			Evidence:  referenceEvidenceCandidate{StartLine: 3, EndLine: 3, ContentStartLine: 3, ContentEndLine: 3, Column: 10, Content: "3: MakeBucket()"},
		},
		{
			Container: domain.SymbolLocation{Kind: int(protocol.SymbolKindFunction), Path: "UseMakeBucketTwice", File: "references.go", StartLine: 2, EndLine: 6},
			Evidence:  referenceEvidenceCandidate{StartLine: 3, EndLine: 3, ContentStartLine: 3, ContentEndLine: 3, Column: 18, Content: "3: MakeBucket(MakeBucket())"},
		},
		{
			Container: domain.SymbolLocation{Kind: int(protocol.SymbolKindFunction), Path: "UseMakeBucketTwice", File: "references.go", StartLine: 2, EndLine: 6},
			Evidence:  referenceEvidenceCandidate{StartLine: 5, EndLine: 5, ContentStartLine: 5, ContentEndLine: 5, Column: 4, Content: "5: MakeBucket()"},
		},
	}

	grouped := groupReferenceMatches(matches)

	require.Len(t, grouped, 1)
	assert.Equal(t, 3, grouped[0].ContentStartLine)
	assert.Equal(t, 3, grouped[0].ContentEndLine)
	assert.Equal(t, "3: MakeBucket()", grouped[0].Content)
}
