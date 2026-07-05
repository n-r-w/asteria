package lspclangd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBackgroundIndexProgressReportsIncompleteUntilIndexingEnds proves that active clangd indexing marks references incomplete.
func TestBackgroundIndexProgressReportsIncompleteUntilIndexingEnds(t *testing.T) {
	t.Parallel()

	const workspaceRoot = "/workspace"
	progress := newBackgroundIndexProgress()
	assert.True(t, progress.incomplete(workspaceRoot))

	progress.recordProgress(workspaceRoot, []byte(`{"token":"background-index","value":{"kind":"begin","title":"indexing"}}`))
	assert.True(t, progress.incomplete(workspaceRoot))

	progress.recordProgress(workspaceRoot, []byte(`{"token":"background-index","value":{"kind":"end"}}`))
	assert.False(t, progress.incomplete(workspaceRoot))
}

// TestBackgroundIndexProgressIgnoresUnrelatedProgress prevents non-index work from changing reference completeness.
func TestBackgroundIndexProgressIgnoresUnrelatedProgress(t *testing.T) {
	t.Parallel()

	const workspaceRoot = "/workspace"
	progress := newBackgroundIndexProgress()
	progress.recordProgress(workspaceRoot, []byte(`{"token":"other","value":{"kind":"begin","title":"diagnostics"}}`))
	assert.True(t, progress.incomplete(workspaceRoot))

	progress.recordProgress(workspaceRoot, []byte(`{"token":"background-index","value":{"kind":"begin","message":"indexing: 1/2"}}`))
	progress.recordProgress(workspaceRoot, []byte(`{"token":"other","value":{"kind":"end"}}`))
	assert.True(t, progress.incomplete(workspaceRoot))

	progress.recordProgress(workspaceRoot, []byte(`{"token":"background-index","value":{"kind":"end"}}`))
	assert.False(t, progress.incomplete(workspaceRoot))
}

// TestBackgroundIndexProgressIsWorkspaceScoped prevents one workspace's idle index from clearing another workspace warning.
func TestBackgroundIndexProgressIsWorkspaceScoped(t *testing.T) {
	t.Parallel()

	progress := newBackgroundIndexProgress()
	progress.recordProgress("/workspace-a", []byte(`{"token":"background-index","value":{"kind":"begin","title":"indexing"}}`))
	progress.recordProgress("/workspace-a", []byte(`{"token":"background-index","value":{"kind":"end"}}`))

	assert.False(t, progress.incomplete("/workspace-a"))
	assert.True(t, progress.incomplete("/workspace-b"))
}
