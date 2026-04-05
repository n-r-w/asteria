package lspphpactor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPHPActorMemberReferenceTarget proves that member references use the class FQCN when the declaration file
// is namespaced, which is required for Phpactor CLI lookups in multi-class files.
func TestPHPActorMemberReferenceTarget(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	require.NoError(
		t,
		os.WriteFile(
			filepath.Join(workspaceRoot, "namespaced.php"),
			[]byte("<?php\n\nnamespace Acme\\Model;\n\nfinal class AdvancedBucket\n{\n    public function createTagged(): self\n    {\n        return $this;\n    }\n}\n"),
			0o600,
		),
	)

	referenceTarget, memberName, ok, err := phpactorMemberReferenceTarget(workspaceRoot, &domain.FoundSymbol{
		Kind:      6,
		Body:      "",
		Info:      "",
		Path:      "AdvancedBucket/createTagged",
		File:      "namespaced.php",
		StartLine: 4,
		EndLine:   9,
	})
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, `Acme\Model\AdvancedBucket`, referenceTarget)
	assert.Equal(t, "createTagged", memberName)
}

// TestPHPImportedAliases proves that the class-reference text fallback keeps alias-based usages visible when the
// target class is imported under a different local name.
func TestPHPImportedAliases(t *testing.T) {
	t.Parallel()

	aliases := phpImportedAliases(`<?php

use Acme\Model\AdvancedBucket as ImportedBucket;
use Acme\Model\OtherBucket;
`, `Acme\Model\AdvancedBucket`)

	assert.Equal(t, []string{"ImportedBucket"}, aliases)
}

// TestCollectPHPClassReferenceRowsUsesImportedAlias proves that the class-reference text fallback keeps alias
// call sites visible when Phpactor cannot address a class declaration in a multi-class file directly.
func TestCollectPHPClassReferenceRowsUsesImportedAlias(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	require.NoError(
		t,
		os.WriteFile(
			filepath.Join(workspaceRoot, "namespaced.php"),
			[]byte("<?php\n\nnamespace Acme\\Model;\n\nfinal class AdvancedBucket {}\n"),
			0o600,
		),
	)
	require.NoError(
		t,
		os.WriteFile(
			filepath.Join(workspaceRoot, "consumer.php"),
			[]byte("<?php\n\nnamespace Acme\\Consumer;\n\nuse Acme\\Model\\AdvancedBucket as ImportedBucket;\n\nfunction use_bucket(): string\n{\n    return ImportedBucket::class;\n}\n"),
			0o600,
		),
	)

	referenceRows, err := collectPHPClassReferenceRows(workspaceRoot, &domain.FoundSymbol{
		Kind:      5,
		Body:      "",
		Info:      "",
		Path:      "AdvancedBucket",
		File:      "namespaced.php",
		StartLine: 4,
		EndLine:   4,
	})
	require.NoError(t, err)
	assert.Contains(t, collectReferenceRowFiles(referenceRows), "consumer.php")
}

func collectReferenceRowFiles(referenceRows []phpactorReferenceRow) []string {
	files := make([]string, 0, len(referenceRows))
	for _, referenceRow := range referenceRows {
		files = append(files, referenceRow.File)
	}

	return files
}
