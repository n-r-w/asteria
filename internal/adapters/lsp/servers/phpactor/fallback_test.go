package lspphpactor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParsePHPActorReferenceRows proves that the phpactor table parser keeps only real reference rows and
// converts line numbers to Asteria's 0-based convention.
func TestParsePHPActorReferenceRows(t *testing.T) {
	t.Parallel()

	rows, err := parsePHPActorReferenceRows(`
# References:
+-------------+----+----------------------------------+-----+-----+
| Path        | LN | Line                             | OS  | OE  |
+-------------+----+----------------------------------+-----+-----+
| fixture.php | 7  |     private string $⟶$value⟵     | 74  | 79  |
| fixture.php | 11 |         $this->⟶value⟵ = $value; | 150 | 155 |
| fixture.php | 16 |         return $this->⟶value⟵;   | 240 | 245 |
+-------------+----+----------------------------------+-----+-----+
`)
	require.NoError(t, err)
	assert.Equal(t, []phpactorReferenceRow{
		{File: "fixture.php", Line: 6},
		{File: "fixture.php", Line: 10},
		{File: "fixture.php", Line: 15},
	}, rows)
}

// TestCollectPHPFileConstants proves that the constant fallback restores top-level PHP constants without
// accidentally treating class constants as file-level symbols.
func TestCollectPHPFileConstants(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	filePath := filepath.Join(workspaceRoot, "fixture.php")
	require.NoError(t, os.WriteFile(filePath, []byte(`<?php

const FIXTURE_STAMP = 'stamp';

class Bucket
{
    public const CLASS_STAMP = 'class';
}
`), 0o600))

	constants, err := collectPHPFileConstants(workspaceRoot, "fixture.php")
	require.NoError(t, err)
	assert.Equal(t, []phpConstantSymbol{{
		Name:        "FIXTURE_STAMP",
		File:        "fixture.php",
		StartLine:   2,
		EndLine:     2,
		Declaration: "const FIXTURE_STAMP = 'stamp';",
	}}, constants)
}

// TestAugmentFindSymbolWithPHPConstants proves that the adapter can surface an exact top-level constant match
// even when the primary stdlsp result is empty.
func TestAugmentFindSymbolWithPHPConstants(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	filePath := filepath.Join(workspaceRoot, "fixture.php")
	require.NoError(t, os.WriteFile(filePath, []byte("<?php\n\nconst FIXTURE_STAMP = 'stamp';\n"), 0o600))

	result, err := augmentFindSymbolWithPHPConstants(workspaceRoot, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:              "/FIXTURE_STAMP",
			IncludeKinds:      nil,
			ExcludeKinds:      nil,
			Depth:             0,
			IncludeBody:       false,
			IncludeInfo:       false,
			SubstringMatching: false,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         "fixture.php",
	}, domain.FindSymbolResult{Symbols: nil})
	require.NoError(t, err)
	require.Len(t, result.Symbols, 1)
	assert.Equal(t, domain.FoundSymbol{
		Kind:      14,
		Body:      "",
		Info:      "",
		Path:      "FIXTURE_STAMP",
		File:      "fixture.php",
		StartLine: 2,
		EndLine:   2,
	}, result.Symbols[0])
}

// TestPHPActorPropertyReferenceTarget proves that the property-reference fallback uses the declaration file path
// as phpactor's first CLI argument and strips duplicate discriminators from the property leaf.
func TestPHPActorPropertyReferenceTarget(t *testing.T) {
	t.Parallel()

	referenceTarget, propertyName, ok := phpactorPropertyReferenceTarget(&domain.FoundSymbol{
		Kind:      7,
		Body:      "",
		Info:      "",
		Path:      "PerHotelCommissionsService@18:0/commissionHotelsKafkaTopic@37:4",
		File:      filepath.ToSlash(filepath.Join("core", "src", "Pricing", "Client", "PerHotelCommissionsService.php")),
		StartLine: 37,
		EndLine:   37,
	})
	require.True(t, ok)
	assert.Equal(
		t,
		filepath.ToSlash(filepath.Join("core", "src", "Pricing", "Client", "PerHotelCommissionsService.php")),
		referenceTarget,
	)
	assert.Equal(t, "commissionHotelsKafkaTopic", propertyName)
}
