package stdlsp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/n-r-w/asteria/internal/domain"
	"go.lsp.dev/protocol"
)

// decodedRawSymbol keeps both raw LSP payload shapes behind one adapter.
// This keeps shared mapping code format-agnostic.
type decodedRawSymbol struct {
	// documentSymbol stores hierarchical symbol payloads.
	documentSymbol *protocol.DocumentSymbol
	// symbolInformation stores flat symbol payloads.
	symbolInformation *protocol.SymbolInformation
}

// mapRawSymbolsToOverview decodes the LSP response shape and keeps the result contract stable for callers.
func mapRawSymbolsToOverview(
	relativePath string,
	depth int,
	rawSymbols []json.RawMessage,
	buildNamePath NamePathBuilder,
) (domain.GetSymbolsOverviewResult, error) {
	nodes, err := mapRawSymbolsToTree(relativePath, rawSymbols, buildNamePath)
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}

	result := domain.GetSymbolsOverviewResult{Symbols: make([]domain.SymbolLocation, 0)}
	appendNodeOverview(&result.Symbols, nodes, depth)
	sort.SliceStable(result.Symbols, func(leftIdx, rightIdx int) bool {
		left := result.Symbols[leftIdx]
		right := result.Symbols[rightIdx]
		if left.StartLine != right.StartLine {
			return left.StartLine < right.StartLine
		}
		if left.EndLine != right.EndLine {
			return left.EndLine < right.EndLine
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}

		return left.Kind < right.Kind
	})

	return result, nil
}

// mapRawSymbolsToTree decodes raw LSP symbols into one normalized tree that adapters can reuse.
func mapRawSymbolsToTree(
	relativePath string,
	rawSymbols []json.RawMessage,
	buildNamePath NamePathBuilder,
) ([]*node, error) {
	builder := normalizeNamePathBuilder(buildNamePath)
	result := make([]*node, 0, len(rawSymbols))
	for _, rawSymbol := range rawSymbols {
		decodedSymbol, err := decodeRawSymbol(rawSymbol)
		if err != nil {
			return nil, err
		}

		result = append(result, decodedSymbol.treeNode(relativePath, builder))
	}
	rebuildDuplicateNamePaths(result, "")

	return result, nil
}

// decodeRawSymbol decodes one raw LSP symbol into the shared shape used by overview and tree builders.
func decodeRawSymbol(rawSymbol json.RawMessage) (decodedRawSymbol, error) {
	fields := make(map[string]json.RawMessage)
	if err := json.Unmarshal(rawSymbol, &fields); err != nil {
		return decodedRawSymbol{}, fmt.Errorf("decode raw symbol: %w", err)
	}

	if _, hasLocation := fields["location"]; hasLocation {
		var symbol protocol.SymbolInformation
		if err := json.Unmarshal(rawSymbol, &symbol); err != nil {
			return decodedRawSymbol{}, fmt.Errorf("decode symbol information: %w", err)
		}

		return decodedRawSymbol{
			documentSymbol:    nil,
			symbolInformation: &symbol,
		}, nil
	}

	var symbol protocol.DocumentSymbol
	if err := json.Unmarshal(rawSymbol, &symbol); err != nil {
		return decodedRawSymbol{}, fmt.Errorf("decode document symbol: %w", err)
	}

	return decodedRawSymbol{
		documentSymbol:    &symbol,
		symbolInformation: nil,
	}, nil
}

// treeNode converts one decoded raw symbol into the normalized tree node shape.
func (s decodedRawSymbol) treeNode(relativePath string, buildNamePath NamePathBuilder) *node {
	if s.symbolInformation != nil {
		return symbolInformationNode(relativePath, s.symbolInformation, buildNamePath)
	}

	return documentSymbolNode(relativePath, "", s.documentSymbol, buildNamePath)
}

// appendNodeOverview flattens one normalized tree into overview rows while preserving the requested descendant depth.
func appendNodeOverview(symbols *[]domain.SymbolLocation, nodes []*node, depth int) {
	for _, node := range nodes {
		startLine, endLine := inclusiveLineBounds(node.Range)
		*symbols = append(*symbols, domain.SymbolLocation{
			Kind:      node.Kind,
			Path:      node.NamePath,
			File:      node.RelativePath,
			StartLine: startLine,
			EndLine:   endLine,
		})
		if depth <= 0 {
			continue
		}

		appendNodeOverview(symbols, node.Children, depth-1)
	}
}

// collectMatchedNodesForRequest walks one normalized symbol tree and returns all distinct search matches.
func collectMatchedNodesForRequest(
	nodes []*node,
	matcher *namePathMatcher,
	depth int,
	includeKinds []int,
	excludeKinds []int,
) []*node {
	result := make([]*node, 0)
	matchSet := make(map[string]struct{})
	collectMatchedNodes(&result, nodes, matcher, depth, includeKinds, excludeKinds, matchSet)

	return result
}

// findUniqueNode resolves one unique symbol inside one file-local tree or returns a helpful ambiguity error.
func findUniqueNode(nodes []*node, namePath string) (*node, error) {
	matcher := newNamePathMatcher(namePath, false)
	candidates := collectMatchingNodes(nodes, matcher)
	if len(candidates) == 0 {
		return nil, domain.NewSafeError(fmt.Sprintf("no symbol matches %q", namePath), nil)
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}

	trimmedNamePath := strings.TrimPrefix(strings.TrimSpace(namePath), "/")
	exactMatches := make([]*node, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.NamePath == trimmedNamePath {
			exactMatches = append(exactMatches, candidate)
		}
	}
	if len(exactMatches) == 1 {
		return exactMatches[0], nil
	}

	descriptions := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		description := fmt.Sprintf(
			"%s (%s:%d)",
			candidate.NamePath,
			candidate.RelativePath,
			int(candidate.SelectionRange.Start.Line),
		)
		descriptions = append(descriptions, description)
	}
	sort.Strings(descriptions)

	return nil, domain.NewSafeError(
		fmt.Sprintf(
			"multiple symbols match %q; use a more specific symbol_path. Candidates: %s",
			namePath,
			strings.Join(descriptions, ", "),
		),
		nil,
	)
}

// collectMatchingNodes gathers all tree nodes whose normalized name paths satisfy the requested matcher.
func collectMatchingNodes(nodes []*node, matcher *namePathMatcher) []*node {
	result := make([]*node, 0)
	for _, node := range nodes {
		if matcher.matches(node.NamePath) {
			result = append(result, node)
		}
		result = append(result, collectMatchingNodes(node.Children, matcher)...)
	}

	return result
}

// findContainingNode resolves the innermost symbol whose full range contains the requested position.
func findContainingNode(nodes []*node, position protocol.Position) (*node, bool) {
	for _, node := range nodes {
		if !positionInRange(position, node.Range) {
			continue
		}

		if child, ok := findContainingNode(node.Children, position); ok {
			return child, true
		}

		return node, true
	}

	return nil, false
}

// positionInRange checks LSP positions with an inclusive start and exclusive end, matching protocol range semantics.
func positionInRange(position protocol.Position, targetRange protocol.Range) bool {
	return comparePositions(position, targetRange.Start) >= 0 && comparePositions(position, targetRange.End) < 0
}

// comparePositions provides stable ordering for line/column comparisons without extra allocations.
func comparePositions(left, right protocol.Position) int {
	if left.Line != right.Line {
		if left.Line < right.Line {
			return -1
		}

		return 1
	}
	if left.Character < right.Character {
		return -1
	}
	if left.Character > right.Character {
		return 1
	}

	return 0
}

// rebuildDuplicateNamePaths rewrites sibling paths so duplicate names become uniquely addressable in exported results.
func rebuildDuplicateNamePaths(nodes []*node, parentPath string) {
	if len(nodes) == 0 {
		return
	}

	baseNames := make(map[*node]string, len(nodes))
	groupedByFullPath := make(map[string][]*node, len(nodes))
	for _, node := range nodes {
		baseName := namePathLeaf(node.NamePath)
		currentParentPath := parentPath
		if currentParentPath == "" {
			currentParentPath = namePathParent(node.NamePath)
		}

		baseNames[node] = baseName
		groupKey := JoinNamePath(currentParentPath, baseName)
		groupedByFullPath[groupKey] = append(groupedByFullPath[groupKey], node)
	}

	for _, node := range nodes {
		baseName := baseNames[node]
		currentParentPath := parentPath
		if currentParentPath == "" {
			currentParentPath = namePathParent(node.NamePath)
		}

		groupKey := JoinNamePath(currentParentPath, baseName)
		pathComponent := baseName
		if len(groupedByFullPath[groupKey]) > 1 {
			pathComponent = disambiguatedNamePathComponent(baseName, node.SelectionRange.Start)
		}
		node.NamePath = JoinNamePath(currentParentPath, pathComponent)
		rebuildDuplicateNamePaths(node.Children, node.NamePath)
	}
}

// namePathParent returns the path prefix before the final slash-delimited symbol component.
func namePathParent(namePath string) string {
	separatorIndex := strings.LastIndex(namePath, "/")
	if separatorIndex <= 0 {
		return ""
	}

	return namePath[:separatorIndex]
}

// namePathLeaf returns the final slash-delimited symbol component without changing duplicate suffixes.
func namePathLeaf(namePath string) string {
	separatorIndex := strings.LastIndex(namePath, "/")
	if separatorIndex < 0 || separatorIndex >= len(namePath)-1 {
		return namePath
	}

	return namePath[separatorIndex+1:]
}

// disambiguatedNamePathComponent appends one stable line:character suffix to duplicate sibling names.
func disambiguatedNamePathComponent(baseName string, position protocol.Position) string {
	return fmt.Sprintf(
		"%s%s%d%s%d",
		baseName,
		namePathDiscriminatorSeparator,
		int(position.Line),
		namePathDiscriminatorValueSeparator,
		int(position.Character),
	)
}

// documentSymbolNode converts one hierarchical document symbol into the normalized search tree shape.
func documentSymbolNode(
	relativePath string,
	parentPath string,
	symbol *protocol.DocumentSymbol,
	buildNamePath NamePathBuilder,
) *node {
	namePath := buildNamePath(parentPath, symbol.Name)
	node := &node{
		Kind:           int(symbol.Kind),
		NamePath:       namePath,
		RelativePath:   relativePath,
		Range:          symbol.Range,
		SelectionRange: symbol.SelectionRange,
		Children:       make([]*node, 0, len(symbol.Children)),
	}
	for childIndex := range symbol.Children {
		node.Children = append(
			node.Children,
			documentSymbolNode(relativePath, namePath, &symbol.Children[childIndex], buildNamePath),
		)
	}

	return node
}

// symbolInformationNode converts flat symbol information into the normalized search tree shape.
func symbolInformationNode(
	relativePath string,
	symbol *protocol.SymbolInformation,
	buildNamePath NamePathBuilder,
) *node {
	parentPath := strings.TrimSpace(symbol.ContainerName)

	return &node{
		Kind:         int(symbol.Kind),
		NamePath:     buildNamePath(parentPath, symbol.Name),
		RelativePath: relativePath,
		Range:        symbol.Location.Range,
		SelectionRange: protocol.Range{
			Start: symbol.Location.Range.Start,
			End:   symbol.Location.Range.Start,
		},
		Children: nil,
	}
}

// collectMatchedNodes preserves the previous adapter semantics while moving tree traversal into shared helpers.
func collectMatchedNodes(
	out *[]*node,
	nodes []*node,
	matcher *namePathMatcher,
	depth int,
	includeKinds []int,
	excludeKinds []int,
	matchSet map[string]struct{},
) {
	for _, node := range nodes {
		if matcher.matches(node.NamePath) && matchesKindFilters(node.Kind, includeKinds, excludeKinds) {
			appendMatchedNodeDescendants(out, node, depth, includeKinds, excludeKinds, matchSet)
		}

		collectMatchedNodes(out, node.Children, matcher, depth, includeKinds, excludeKinds, matchSet)
	}
}

// appendMatchedNodeDescendants flattens one matched symbol and the requested descendant depth into distinct nodes.
func appendMatchedNodeDescendants(
	out *[]*node,
	node *node,
	depth int,
	includeKinds []int,
	excludeKinds []int,
	matchSet map[string]struct{},
) {
	if node == nil {
		return
	}
	appendMatchedNode(out, node, includeKinds, excludeKinds, matchSet)
	if depth <= 0 {
		return
	}

	for _, child := range node.Children {
		appendMatchedNodeDescendants(out, child, depth-1, includeKinds, excludeKinds, matchSet)
	}
}

// appendMatchedNode appends one flattened symbol when it survives filters and has not been added yet.
func appendMatchedNode(
	out *[]*node,
	node *node,
	includeKinds []int,
	excludeKinds []int,
	matchSet map[string]struct{},
) {
	if !matchesKindFilters(node.Kind, includeKinds, excludeKinds) {
		return
	}

	matchKey := fmt.Sprintf(
		"%s\x00%s\x00%d",
		node.NamePath,
		node.RelativePath,
		int(node.SelectionRange.Start.Line)+1,
	)
	if _, exists := matchSet[matchKey]; exists {
		return
	}

	matchSet[matchKey] = struct{}{}
	*out = append(*out, node)
}

// normalizeNamePathBuilder keeps shared helpers usable even when one adapter does not need special normalization.
func normalizeNamePathBuilder(buildNamePath NamePathBuilder) NamePathBuilder {
	if buildNamePath != nil {
		return buildNamePath
	}

	return JoinNamePath
}
