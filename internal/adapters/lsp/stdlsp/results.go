package stdlsp

import (
	"fmt"
	"slices"
	"sort"

	"github.com/n-r-w/asteria/internal/domain"
)

// matchesKindFilters checks whether one symbol kind survives include/exclude filters.
func matchesKindFilters(kind int, includeKinds, excludeKinds []int) bool {
	if slices.Contains(excludeKinds, kind) {
		return false
	}
	if len(includeKinds) == 0 {
		return true
	}

	return slices.Contains(includeKinds, kind)
}

// groupReferenceMatches groups references under the symbol that contains them and keeps one
// deterministic representative reference per resulting symbol.
func groupReferenceMatches(matches []referenceMatch) []domain.ReferencingSymbol {
	type groupedReference struct {
		symbol          domain.ReferencingSymbol
		referenceColumn int
		referenceStart  int
		referenceEnd    int
	}

	groupByKey := make(map[string]*groupedReference)
	for _, match := range matches {
		groupKey := fmt.Sprintf(
			"%s\x00%s\x00%d\x00%d",
			match.Container.Path,
			match.Container.File,
			match.Container.StartLine,
			match.Container.EndLine,
		)
		group, ok := groupByKey[groupKey]
		if !ok {
			group = &groupedReference{
				symbol: domain.ReferencingSymbol{
					Kind:             match.Container.Kind,
					Path:             match.Container.Path,
					File:             match.Container.File,
					ContentStartLine: match.Evidence.ContentStartLine,
					ContentEndLine:   match.Evidence.ContentEndLine,
					Content:          match.Evidence.Content,
				},
				referenceColumn: match.Evidence.Column,
				referenceStart:  match.Evidence.StartLine,
				referenceEnd:    match.Evidence.EndLine,
			}
			groupByKey[groupKey] = group

			continue
		}

		if shouldReplaceReference(group.referenceStart, group.referenceEnd, group.referenceColumn, match.Evidence) {
			group.symbol.ContentStartLine = match.Evidence.ContentStartLine
			group.symbol.ContentEndLine = match.Evidence.ContentEndLine
			group.symbol.Content = match.Evidence.Content
			group.referenceColumn = match.Evidence.Column
			group.referenceStart = match.Evidence.StartLine
			group.referenceEnd = match.Evidence.EndLine
		}
	}

	result := make([]domain.ReferencingSymbol, 0, len(groupByKey))
	for _, group := range groupByKey {
		result = append(result, group.symbol)
	}

	sortReferencingSymbols(result)

	return result
}

// shouldReplaceReference keeps representative reference selection deterministic by preferring
// source order and, for one line, the leftmost reference column.
func shouldReplaceReference(
	currentStartLine int,
	currentEndLine int,
	currentColumn int,
	candidate referenceEvidenceCandidate,
) bool {
	if candidate.StartLine != currentStartLine {
		return candidate.StartLine < currentStartLine
	}
	if candidate.EndLine != currentEndLine {
		return candidate.EndLine < currentEndLine
	}
	if candidate.Column != currentColumn {
		return candidate.Column < currentColumn
	}

	return false
}

// sortFoundSymbols keeps find_symbol output deterministic across filesystem walk order and LSP response order.
func sortFoundSymbols(symbols []domain.FoundSymbol) {
	sort.Slice(symbols, func(leftIndex, rightIndex int) bool {
		left := symbols[leftIndex]
		right := symbols[rightIndex]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.StartLine != right.StartLine {
			return left.StartLine < right.StartLine
		}
		if left.EndLine != right.EndLine {
			return left.EndLine < right.EndLine
		}

		return left.Path < right.Path
	})
}

// sortReferencingSymbols keeps grouped reference output deterministic across files, symbols, and occurrence order.
func sortReferencingSymbols(symbols []domain.ReferencingSymbol) {
	sort.Slice(symbols, func(leftIndex, rightIndex int) bool {
		left := symbols[leftIndex]
		right := symbols[rightIndex]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.ContentStartLine != right.ContentStartLine {
			return left.ContentStartLine < right.ContentStartLine
		}
		if left.ContentEndLine != right.ContentEndLine {
			return left.ContentEndLine < right.ContentEndLine
		}

		return left.Path < right.Path
	})
}
