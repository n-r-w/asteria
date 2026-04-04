package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/n-r-w/asteria/internal/config"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLimitFindSymbolOutputAddsReturnedPercent proves that truncated symbol responses expose only the compact returned percentage.
func TestLimitFindSymbolOutputAddsReturnedPercent(t *testing.T) {
	t.Parallel()

	svc := &Service{ //nolint:exhaustruct // only cfg is relevant for output limiting
		cfg: &config.Config{
			CacheRoot:              "",
			SystemPrompt:           "",
			GetSymbolsOverviewDesc: "",
			FindSymbolDesc:         "",
			FindReferencesDesc:     "",
			ToolTimeout:            10 * time.Second,
			ToolOutputMaxBytes:     120,
			Adapters:               cfgadapters.Config{},
		},
	}
	output := findSymbolOutput{Symbols: []foundSymbolDTO{
		{Kind: 12, Body: "", Info: "", Path: "Alpha", File: "a.go", Range: "0"},
		{Kind: 12, Body: "", Info: "", Path: "Bravo", File: "b.go", Range: "1"},
		{Kind: 12, Body: "", Info: "", Path: "Charlie", File: "c.go", Range: "2"},
		{Kind: 12, Body: "", Info: "", Path: "Delta", File: "d.go", Range: "3"},
	}, ReturnedPercent: 0}

	limited, err := svc.limitFindSymbolOutput(output)
	require.NoError(t, err)
	assert.Len(t, limited.Symbols, 2)
	assert.Equal(t, 50, limited.ReturnedPercent)

	payload, err := json.Marshal(limited)
	require.NoError(t, err)
	assert.JSONEq(t, `{"symbols":[{"kind":12,"path":"Alpha","file":"a.go","range":"0"},{"kind":12,"path":"Bravo","file":"b.go","range":"1"}],"returned_percent":50}`,
		string(payload))
}

// TestLimitFindReferencingSymbolsOutputCountsNestedSymbols proves that returned percentage is based on logical reference objects, not file buckets.
func TestLimitFindReferencingSymbolsOutputCountsNestedSymbols(t *testing.T) {
	t.Parallel()

	svc := &Service{ //nolint:exhaustruct // only cfg is relevant for output limiting
		cfg: &config.Config{
			CacheRoot:              "",
			SystemPrompt:           "",
			GetSymbolsOverviewDesc: "",
			FindSymbolDesc:         "",
			FindReferencesDesc:     "",
			ToolTimeout:            10 * time.Second,
			ToolOutputMaxBytes:     150,
			Adapters:               cfgadapters.Config{},
		},
	}
	output := findReferencingSymbolsOutput{Files: []referencingFileDTO{
		{
			File: "a.go",
			Symbols: []referencingSymbolDTO{
				{Kind: 12, Path: "A/One", Range: "0", Content: "one"},
				{Kind: 12, Path: "A/Two", Range: "1", Content: "two"},
			},
		},
		{
			File: "b.go",
			Symbols: []referencingSymbolDTO{
				{Kind: 12, Path: "B/Three", Range: "2", Content: "three"},
				{Kind: 12, Path: "B/Four", Range: "3", Content: "four"},
			},
		},
	}, ReturnedPercent: 0}

	limited, err := svc.limitFindReferencingSymbolsOutput(output)
	require.NoError(t, err)
	assert.Equal(t, 50, limited.ReturnedPercent)
	assert.Len(t, limited.Files, 1)
	require.Len(t, limited.Files[0].Symbols, 2)
	assert.Equal(t, "A/One", limited.Files[0].Symbols[0].Path)
	assert.Equal(t, "A/Two", limited.Files[0].Symbols[1].Path)
}

// TestTrimOverviewEntriesCountsGroupedSymbols proves that overview truncation uses symbol entries rather than group count.
func TestTrimOverviewEntriesCountsGroupedSymbols(t *testing.T) {
	t.Parallel()

	trimmed := trimOverviewEntries([]overviewKindGroupDTO{
		{
			Kind: 6,
			Symbols: []overviewGroupSymbolDTO{
				{Path: "A", Range: "0"},
				{Path: "B", Range: "1"},
			},
		},
		{
			Kind: 12,
			Symbols: []overviewGroupSymbolDTO{
				{Path: "C", Range: "2"},
			},
		},
	}, 2)

	assert.Equal(t, []overviewKindGroupDTO{{
		Kind: 6,
		Symbols: []overviewGroupSymbolDTO{
			{Path: "A", Range: "0"},
			{Path: "B", Range: "1"},
		},
	}}, trimmed)
}

// TestCalculateReturnedPercentClampsToCompactRange proves that truncation percentage never publishes 0 or 100.
func TestCalculateReturnedPercentClampsToCompactRange(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 1, calculateReturnedPercent(1, 200))
	assert.Equal(t, 99, calculateReturnedPercent(99, 99))
}
