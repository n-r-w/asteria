package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/n-r-w/asteria/internal/config"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
)

// TestLimitFindSymbolOutputAddsReturnedPercent proves that truncated symbol responses expose only
// the compact returned percentage.
func TestLimitFindSymbolOutputAddsReturnedPercent(t *testing.T) {
	t.Parallel()

	svc := &Service{ //nolint:exhaustruct_v5 // only cfg is relevant for output limiting
		cfg: &config.Config{
			CacheRoot:              "",
			GetSymbolsOverviewDesc: "",
			FindSymbolDesc:         "",
			FindReferencesDesc:     "",
			ToolTimeout:            10 * time.Second,
			LSPSessionIdleTimeout:  0,
			ToolOutputMaxBytes:     120,
			Log:                    config.LogConfig{},
			Adapters:               cfgadapters.Config{},
		},
	}
	output := findSymbolOutput{Symbols: []foundSymbolDTO{
		{Kind: 12, Body: "", Info: "", Path: "Alpha", File: "a.go", Range: "1"},
		{Kind: 12, Body: "", Info: "", Path: "Bravo", File: "b.go", Range: "2"},
		{Kind: 12, Body: "", Info: "", Path: "Charlie", File: "c.go", Range: "3"},
		{Kind: 12, Body: "", Info: "", Path: "Delta", File: "d.go", Range: "4"},
	}, ReturnedPercent: 0}

	limited, err := svc.limitFindSymbolOutput(output)
	require.NoError(t, err)
	assert.Len(t, limited.Symbols, 2)
	assert.Equal(t, 50, limited.ReturnedPercent)

	payload, err := json.Marshal(limited)
	require.NoError(t, err)
	assert.JSONEq(
		t,
		`{"symbols":[{"kind":12,"path":"Alpha","file":"a.go","range":"1"},`+
			`{"kind":12,"path":"Bravo","file":"b.go","range":"2"}],"returned_percent":50}`,
		string(payload),
	)
}

// TestLimitFindSymbolOutputReturnsPublicSizeError proves that oversized responses do not expose
// server configuration names.
func TestLimitFindSymbolOutputReturnsPublicSizeError(t *testing.T) {
	t.Parallel()

	svc := &Service{ //nolint:exhaustruct_v5 // only cfg is relevant for output limiting
		cfg: &config.Config{
			CacheRoot:              "",
			GetSymbolsOverviewDesc: "",
			FindSymbolDesc:         "",
			FindReferencesDesc:     "",
			ToolTimeout:            10 * time.Second,
			LSPSessionIdleTimeout:  0,
			ToolOutputMaxBytes:     1,
			Log:                    config.LogConfig{},
			Adapters:               cfgadapters.Config{},
		},
	}

	_, err := svc.limitFindSymbolOutput(findSymbolOutput{
		Symbols:         []foundSymbolDTO{{Kind: 12, Body: "", Info: "", Path: "Alpha", File: "a.go", Range: "1"}},
		ReturnedPercent: 0,
	})
	require.EqualError(t, err, outputTooLargePublicMessage)
}

// TestLimitFindReferencingSymbolsOutputCountsNestedSymbols proves that returned percentage is based
// on logical reference objects, not file buckets.
func TestLimitFindReferencingSymbolsOutputCountsNestedSymbols(t *testing.T) {
	t.Parallel()

	svc := &Service{ //nolint:exhaustruct_v5 // only cfg is relevant for output limiting
		cfg: &config.Config{
			CacheRoot:              "",
			GetSymbolsOverviewDesc: "",
			FindSymbolDesc:         "",
			FindReferencesDesc:     "",
			ToolTimeout:            10 * time.Second,
			LSPSessionIdleTimeout:  0,
			ToolOutputMaxBytes:     150,
			Log:                    config.LogConfig{},
			Adapters:               cfgadapters.Config{},
		},
	}
	output := findReferencingSymbolsOutput{Files: []referencingFileDTO{
		{
			File: "a.go",
			Symbols: []referencingSymbolDTO{
				{Kind: 12, Path: "A/One", Range: "1", Content: "one"},
				{Kind: 12, Path: "A/Two", Range: "2", Content: "two"},
			},
		},
		{
			File: "b.go",
			Symbols: []referencingSymbolDTO{
				{Kind: 12, Path: "B/Three", Range: "3", Content: "three"},
				{Kind: 12, Path: "B/Four", Range: "4", Content: "four"},
			},
		},
	}, Incomplete: false, ReturnedPercent: 0}

	limited, err := svc.limitFindReferencingSymbolsOutput(output)
	require.NoError(t, err)
	assert.Equal(t, 50, limited.ReturnedPercent)
	assert.False(t, limited.Incomplete)
	assert.Len(t, limited.Files, 1)
	require.Len(t, limited.Files[0].Symbols, 2)
	assert.Equal(t, "A/One", limited.Files[0].Symbols[0].Path)
	assert.Equal(t, "A/Two", limited.Files[0].Symbols[1].Path)
}

// TestTrimOverviewEntriesCountsGroupedSymbols proves that overview truncation uses symbol entries
// rather than group count.
func TestTrimOverviewEntriesCountsGroupedSymbols(t *testing.T) {
	t.Parallel()

	trimmed := trimOverviewEntries([]overviewKindGroupDTO{
		{
			Kind: 6,
			Symbols: []overviewGroupSymbolDTO{
				{Path: "A", Range: "1"},
				{Path: "B", Range: "2"},
			},
		},
		{
			Kind: 12,
			Symbols: []overviewGroupSymbolDTO{
				{Path: "C", Range: "3"},
			},
		},
	}, 2)

	assert.Equal(t, []overviewKindGroupDTO{{
		Kind: 6,
		Symbols: []overviewGroupSymbolDTO{
			{Path: "A", Range: "1"},
			{Path: "B", Range: "2"},
		},
	}}, trimmed)
}

// TestCalculateReturnedPercentClampsToCompactRange proves that truncation percentage never publishes 0 or 100.
func TestCalculateReturnedPercentClampsToCompactRange(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 1, calculateReturnedPercent(1, 200))
	assert.Equal(t, 99, calculateReturnedPercent(99, 99))
}
