package server

import (
	"encoding/json"
	"fmt"

	"github.com/n-r-w/asteria/internal/domain"
)

const (
	binarySearchDivisor = 2
	percentBase         = 100
	minReturnedPercent  = 1
	maxReturnedPercent  = percentBase - 1
)

// fitWindow keeps the current binary-search bounds for one output size probe.
type fitWindow struct {
	best int
	high int
	low  int
}

// limitGetSymbolsOverviewOutput trims grouped overview output to the configured byte budget.
func (s *Service) limitGetSymbolsOverviewOutput(output getSymbolsOverviewOutput) (getSymbolsOverviewOutput, error) {
	totalItems := countOverviewEntries(output.Groups)
	return limitOutputByBytes(
		s.cfg.ToolOutputMaxBytes,
		totalItems,
		output,
		getSymbolsOverviewOutput{Groups: make([]overviewKindGroupDTO, 0), ReturnedPercent: 0},
		func(count int) getSymbolsOverviewOutput {
			return getSymbolsOverviewOutput{Groups: trimOverviewEntries(output.Groups, count), ReturnedPercent: 0}
		},
		func(trimmed getSymbolsOverviewOutput, percent int) getSymbolsOverviewOutput {
			trimmed.ReturnedPercent = percent
			return trimmed
		},
	)
}

// limitFindSymbolOutput trims symbol search output to the configured byte budget.
func (s *Service) limitFindSymbolOutput(output findSymbolOutput) (findSymbolOutput, error) {
	totalItems := len(output.Symbols)
	return limitOutputByBytes(
		s.cfg.ToolOutputMaxBytes,
		totalItems,
		output,
		findSymbolOutput{Symbols: make([]foundSymbolDTO, 0), ReturnedPercent: 0},
		func(count int) findSymbolOutput {
			return findSymbolOutput{
				Symbols:         append(make([]foundSymbolDTO, 0, count), output.Symbols[:count]...),
				ReturnedPercent: 0,
			}
		},
		func(trimmed findSymbolOutput, percent int) findSymbolOutput {
			trimmed.ReturnedPercent = percent
			return trimmed
		},
	)
}

// limitFindReferencingSymbolsOutput trims grouped reference output to the configured byte budget.
func (s *Service) limitFindReferencingSymbolsOutput(
	output findReferencingSymbolsOutput,
) (findReferencingSymbolsOutput, error) {
	totalItems := countReferencingEntries(output.Files)
	return limitOutputByBytes(
		s.cfg.ToolOutputMaxBytes,
		totalItems,
		output,
		findReferencingSymbolsOutput{Files: make([]referencingFileDTO, 0), ReturnedPercent: 0},
		func(count int) findReferencingSymbolsOutput {
			return findReferencingSymbolsOutput{Files: trimReferencingEntries(output.Files, count), ReturnedPercent: 0}
		},
		func(trimmed findReferencingSymbolsOutput, percent int) findReferencingSymbolsOutput {
			trimmed.ReturnedPercent = percent
			return trimmed
		},
	)
}

// limitOutputByBytes keeps the largest prefix of logical result objects that fits into the configured output budget.
func limitOutputByBytes[T any](
	maxBytes int,
	totalItems int,
	fullOutput T,
	emptyOutput T,
	trim func(count int) T,
	setReturnedPercent func(output T, percent int) T,
) (T, error) {
	if maxBytes <= 0 || totalItems == 0 {
		return fullOutput, nil
	}

	fullBytes, err := marshalSize(fullOutput)
	if err != nil {
		return zeroValue[T](), fmt.Errorf("marshal full output: %w", err)
	}
	if fullBytes <= maxBytes {
		return fullOutput, nil
	}

	baseBytes, err := marshalSize(emptyOutput)
	if err != nil {
		return zeroValue[T](), fmt.Errorf("marshal empty output: %w", err)
	}
	if baseBytes >= maxBytes {
		return zeroValue[T](), domain.NewSafeError(
			"tool response exceeds minimum size budget; narrow the query or increase ASTERIAMCP_TOOL_OUTPUT_MAX_BYTES",
			fmt.Errorf("base output size %d exceeds configured maximum %d", baseBytes, maxBytes),
		)
	}

	avgItemBytes := estimateAverageItemBytes(fullBytes, baseBytes, totalItems)
	window, err := newFitWindow(maxBytes, baseBytes, totalItems, avgItemBytes, trim)
	if err != nil {
		return zeroValue[T](), err
	}

	bestCount, err := findBestFitCount(maxBytes, window, trim)
	if err != nil {
		return zeroValue[T](), err
	}

	if bestCount <= 0 {
		return zeroValue[T](), domain.NewSafeError(
			"tool response exceeds minimum size budget; narrow the query or increase ASTERIAMCP_TOOL_OUTPUT_MAX_BYTES",
			fmt.Errorf("no logical result objects fit within %d bytes", maxBytes),
		)
	}

	trimmed := trim(bestCount)
	return setReturnedPercent(trimmed, calculateReturnedPercent(bestCount, totalItems)), nil
}

// estimateAverageItemBytes approximates the average serialized cost of one logical result object.
func estimateAverageItemBytes(fullBytes, baseBytes, totalItems int) int {
	bytesPerItem := fullBytes - baseBytes
	if bytesPerItem <= 0 {
		bytesPerItem = totalItems
	}

	return max(1, bytesPerItem/totalItems)
}

// newFitWindow probes the estimated logical item count and builds the initial binary-search bounds.
func newFitWindow[T any](
	maxBytes int,
	baseBytes int,
	totalItems int,
	avgItemBytes int,
	trim func(count int) T,
) (fitWindow, error) {
	estimatedCount := max(min((maxBytes-baseBytes)/avgItemBytes, totalItems), 0)

	window := fitWindow{best: 0, high: totalItems, low: 0}
	if estimatedCount <= 0 || estimatedCount >= totalItems {
		return window, nil
	}

	estimatedOutput := trim(estimatedCount)
	estimatedBytes, marshalErr := marshalSize(estimatedOutput)
	if marshalErr != nil {
		return fitWindow{}, fmt.Errorf("marshal estimated output: %w", marshalErr)
	}

	if estimatedBytes <= maxBytes {
		window.best = estimatedCount
		window.low = estimatedCount + 1
		return window, nil
	}

	window.high = estimatedCount - 1
	return window, nil
}

// findBestFitCount uses binary search to find the largest logical item count that fits into the byte budget.
func findBestFitCount[T any](maxBytes int, window fitWindow, trim func(count int) T) (int, error) {
	for window.low <= window.high {
		mid := window.low + (window.high-window.low)/binarySearchDivisor
		candidate := trim(mid)
		candidateBytes, marshalErr := marshalSize(candidate)
		if marshalErr != nil {
			return 0, fmt.Errorf("marshal truncated output: %w", marshalErr)
		}

		if candidateBytes <= maxBytes {
			window.best = mid
			window.low = mid + 1
			continue
		}

		window.high = mid - 1
	}

	return window.best, nil
}

// countOverviewEntries counts logical overview result objects across all grouped kinds.
func countOverviewEntries(groups []overviewKindGroupDTO) int {
	total := 0
	for _, group := range groups {
		total += len(group.Symbols)
	}

	return total
}

// trimOverviewEntries keeps the first count logical overview result objects while preserving group order.
func trimOverviewEntries(groups []overviewKindGroupDTO, count int) []overviewKindGroupDTO {
	trimmed := make([]overviewKindGroupDTO, 0, len(groups))
	remaining := count
	for _, group := range groups {
		if remaining <= 0 {
			break
		}

		take := min(len(group.Symbols), remaining)
		if take == 0 {
			continue
		}

		trimmed = append(trimmed, overviewKindGroupDTO{
			Kind:    group.Kind,
			Symbols: append(make([]overviewGroupSymbolDTO, 0, take), group.Symbols[:take]...),
		})
		remaining -= take
	}

	return trimmed
}

// countReferencingEntries counts logical reference result objects across all file buckets.
func countReferencingEntries(files []referencingFileDTO) int {
	total := 0
	for _, file := range files {
		total += len(file.Symbols)
	}

	return total
}

// trimReferencingEntries keeps the first count logical reference result objects while preserving file grouping.
func trimReferencingEntries(files []referencingFileDTO, count int) []referencingFileDTO {
	trimmed := make([]referencingFileDTO, 0, len(files))
	remaining := count
	for _, file := range files {
		if remaining <= 0 {
			break
		}

		take := min(len(file.Symbols), remaining)
		if take == 0 {
			continue
		}

		trimmed = append(trimmed, referencingFileDTO{
			File:    file.File,
			Symbols: append(make([]referencingSymbolDTO, 0, take), file.Symbols[:take]...),
		})
		remaining -= take
	}

	return trimmed
}

// calculateReturnedPercent converts the returned logical object count into a compact 1..99 percentage.
func calculateReturnedPercent(returnedItems, totalItems int) int {
	if totalItems <= 0 || returnedItems <= 0 {
		return 0
	}

	percent := (returnedItems * percentBase) / totalItems
	if percent < minReturnedPercent {
		return minReturnedPercent
	}
	if percent >= percentBase {
		return maxReturnedPercent
	}

	return percent
}

// marshalSize returns the serialized JSON size in bytes for one transport object.
func marshalSize(value any) (int, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return 0, err
	}

	return len(payload), nil
}

// zeroValue returns the zero value for one generic type.
func zeroValue[T any]() T {
	var zero T
	return zero
}
