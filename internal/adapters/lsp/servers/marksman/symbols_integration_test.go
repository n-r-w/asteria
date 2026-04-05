//go:build integration_tests

package lspmarksman

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	lsphelpers "github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	marksmanLiveWaitTimeout = 10 * time.Second
	marksmanLiveWaitTick    = 100 * time.Millisecond
)

// TestIntegrationServiceGetSymbolsOverviewReturnsHeadingTree proves that the live Marksman-backed overview
// exposes hierarchical Markdown headings with stable name paths.
func TestIntegrationServiceGetSymbolsOverviewReturnsHeadingTree(t *testing.T) {
	workspaceRoot := prepareMarksmanWorkspace(t)
	service := newIntegrationService(t)

	result, err := service.GetSymbolsOverview(t.Context(), &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "README.md",
	})
	require.NoError(t, err)

	assertOverviewContainsPath(t, result.Symbols, "README.md", "Project Overview")
	assertOverviewContainsPath(t, result.Symbols, "README.md", "Project Overview/Usage")
	assertOverviewContainsPath(t, result.Symbols, "README.md", "Project Overview/Links")
}

// TestIntegrationServiceFindSymbolResolvesNestedHeading proves that a nested Markdown heading is searchable
// through the canonical path built from heading hierarchy.
func TestIntegrationServiceFindSymbolResolvesNestedHeading(t *testing.T) {
	workspaceRoot := prepareMarksmanWorkspace(t)
	service := newIntegrationService(t)

	result, err := service.FindSymbol(t.Context(), &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "Guide/Installation"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "guide.markdown",
	})
	require.NoError(t, err)

	symbol, ok := findFoundSymbol(result.Symbols, "Guide/Installation")
	require.True(t, ok, "expected nested heading match, got %#v", result.Symbols)
	assert.Equal(t, "guide.markdown", symbol.File)
}

// TestIntegrationServiceFindReferencingSymbolsGroupsMarkdownLinks proves that heading references from links
// are grouped by the smallest containing Markdown section instead of repeated per raw link location.
func TestIntegrationServiceFindReferencingSymbolsGroupsMarkdownLinks(t *testing.T) {
	workspaceRoot := prepareMarksmanWorkspace(t)
	service := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(t.Context(), &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "Guide/Installation"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "guide.markdown",
	})
	require.NoError(t, err)
	require.Len(t, result.Symbols, 2, "duplicate links from one section must collapse into one grouped reference")

	usageSymbol, ok := findReferencingSymbol(result.Symbols, "Project Overview/Usage")
	require.True(t, ok, "expected usage section reference, got %#v", result.Symbols)
	assert.Equal(t, "README.md", usageSymbol.File)
	assert.Contains(t, usageSymbol.Content, "guide.markdown#installation")

	linksSymbol, ok := findReferencingSymbol(result.Symbols, "Project Overview/Links")
	require.True(t, ok, "expected links section reference, got %#v", result.Symbols)
	assert.Equal(t, "README.md", linksSymbol.File)
	assert.Contains(t, linksSymbol.Content, "guide.markdown#installation")
}

// TestIntegrationServiceFindReferencingSymbolsGroupsSameFileHeadingLinks proves that Marksman resolves
// same-file heading links and still groups multiple raw links under one containing Markdown section.
func TestIntegrationServiceFindReferencingSymbolsGroupsSameFileHeadingLinks(t *testing.T) {
	workspaceRoot := prepareMarksmanWorkspace(t)
	service := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(t.Context(), &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "Project Overview/Links"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "README.md",
	})
	require.NoError(t, err)
	require.Len(t, result.Symbols, 1, "same-file duplicate links must collapse into one grouped reference")

	selfLinksSymbol, ok := findReferencingSymbol(result.Symbols, "Project Overview/Self Links")
	require.True(t, ok, "expected self-links section reference, got %#v", result.Symbols)
	assert.Equal(t, "README.md", selfLinksSymbol.File)
	assert.Contains(t, selfLinksSymbol.Content, "#links")
}

// TestIntegrationServiceFindReferencingSymbolsReturnsEmptyForUnreferencedHeading proves that an existing
// Markdown heading with no incoming links returns an empty reference set instead of noise.
func TestIntegrationServiceFindReferencingSymbolsReturnsEmptyForUnreferencedHeading(t *testing.T) {
	workspaceRoot := prepareMarksmanWorkspace(t)
	service := newIntegrationService(t)

	result, err := service.FindReferencingSymbols(t.Context(), &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "Project Overview/Unused"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "README.md",
	})
	require.NoError(t, err)
	assert.Empty(t, result.Symbols)
}

// TestIntegrationServiceFindSymbolUpdatesMarkdownFileWithoutRestart proves that a live Marksman session sees a
// newly created Markdown file and then makes the renamed heading searchable without restarting the server.
func TestIntegrationServiceFindSymbolUpdatesMarkdownFileWithoutRestart(t *testing.T) {
	workspaceRoot := prepareMarksmanWorkspace(t)
	service := newIntegrationService(t)

	_, err := service.GetSymbolsOverview(t.Context(), &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "README.md",
	})
	require.NoError(t, err)

	writeMarksmanWorkspaceFile(t, workspaceRoot, "watcher_probe.md", "# Watcher Added\n\nInitial content.\n")

	require.Eventually(t, func() bool {
		result, findErr := service.FindSymbol(t.Context(), &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{Path: "Watcher Added"},
			WorkspaceRoot:    workspaceRoot,
			Scope:            "watcher_probe.md",
		})
		if findErr != nil {
			return false
		}

		symbol, ok := findFoundSymbol(result.Symbols, "Watcher Added")

		return ok && symbol.File == "watcher_probe.md"
	}, marksmanLiveWaitTimeout, marksmanLiveWaitTick)

	writeMarksmanWorkspaceFile(t, workspaceRoot, "watcher_probe.md", "# Watcher Renamed\n\nRenamed content.\n")

	require.Eventually(t, func() bool {
		newResult, newErr := service.FindSymbol(t.Context(), &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{Path: "Watcher Renamed"},
			WorkspaceRoot:    workspaceRoot,
			Scope:            "watcher_probe.md",
		})
		if newErr != nil {
			return false
		}

		newSymbol, newFound := findFoundSymbol(newResult.Symbols, "Watcher Renamed")

		return newFound && newSymbol.File == "watcher_probe.md"
	}, marksmanLiveWaitTimeout, marksmanLiveWaitTick)
}

// TestIntegrationServiceGetSymbolsOverviewUpdatesMarkdownFileWithoutRestart proves that overview output refreshes
// after an on-disk Markdown heading rename inside the same live Marksman session.
func TestIntegrationServiceGetSymbolsOverviewUpdatesMarkdownFileWithoutRestart(t *testing.T) {
	workspaceRoot := prepareMarksmanWorkspace(t)
	service := newIntegrationService(t)

	_, err := service.GetSymbolsOverview(t.Context(), &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            workspaceRoot,
		File:                     "README.md",
	})
	require.NoError(t, err)

	writeMarksmanWorkspaceFile(t, workspaceRoot, "watcher_probe.md", "# Watcher Added\n\nInitial content.\n")

	require.Eventually(t, func() bool {
		result, overviewErr := service.GetSymbolsOverview(t.Context(), &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
			WorkspaceRoot:            workspaceRoot,
			File:                     "watcher_probe.md",
		})
		if overviewErr != nil {
			return false
		}

		_, ok := lsphelpers.FindOverviewSymbol(result.Symbols, "Watcher Added")

		return ok
	}, marksmanLiveWaitTimeout, marksmanLiveWaitTick)

	writeMarksmanWorkspaceFile(t, workspaceRoot, "watcher_probe.md", "# Watcher Renamed\n\nRenamed content.\n")

	require.Eventually(t, func() bool {
		result, overviewErr := service.GetSymbolsOverview(t.Context(), &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
			WorkspaceRoot:            workspaceRoot,
			File:                     "watcher_probe.md",
		})
		if overviewErr != nil {
			return false
		}

		_, oldFound := lsphelpers.FindOverviewSymbol(result.Symbols, "Watcher Added")
		_, newFound := lsphelpers.FindOverviewSymbol(result.Symbols, "Watcher Renamed")

		return !oldFound && newFound
	}, marksmanLiveWaitTimeout, marksmanLiveWaitTick)
}

// newIntegrationService keeps live-test setup focused on the adapter under test.
func newIntegrationService(t *testing.T) *Service {
	t.Helper()

	service, err := New()
	require.NoError(t, err)

	ctx := t.Context()
	t.Cleanup(func() {
		require.NoError(t, service.Close(ctx))
	})

	return service
}

// prepareMarksmanWorkspace copies the tracked fixture into temp storage and marks that temp copy as one VCS-backed
// project root so cross-file references reflect a real repository instead of a tracked test-only config file.
func prepareMarksmanWorkspace(t *testing.T) string {
	t.Helper()

	requireMarksmanInstalled(t)
	requireGitInstalled(t)

	trackedRoot := trackedMarksmanFixtureRoot(t)
	workspaceRoot := filepath.Join(t.TempDir(), "basic")
	require.NoError(t, copyMarksmanFixtureTree(trackedRoot, workspaceRoot))
	require.NoError(t, initMarksmanWorkspaceGitRoot(t, workspaceRoot))

	return workspaceRoot
}

// trackedMarksmanFixtureRoot resolves the repository fixture root before tests copy it into temp storage.
func trackedMarksmanFixtureRoot(t *testing.T) string {
	t.Helper()

	fixtureRoot, err := filepath.Abs(filepath.Join("testdata", "basic"))
	require.NoError(t, err)

	return fixtureRoot
}

// requireMarksmanInstalled fails fast with a clear message when the external Marksman prerequisite is missing.
func requireMarksmanInstalled(t *testing.T) {
	t.Helper()

	_, err := exec.LookPath(marksmanServerName)
	require.NoError(t, err, "marksman must be installed and available in PATH")
}

// requireGitInstalled fails fast when the live integration workspace cannot emulate a normal repository root.
func requireGitInstalled(t *testing.T) {
	t.Helper()

	_, err := exec.LookPath("git")
	require.NoError(t, err, "git must be installed and available in PATH")
}

// copyMarksmanFixtureTree recreates the tracked Markdown fixture tree under temp storage without mutating source-controlled data.
func copyMarksmanFixtureTree(sourceRoot string, destinationRoot string) error {
	return filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		relativePath, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}

		if relativePath == "." {
			return os.MkdirAll(destinationRoot, 0o755)
		}

		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}

		destinationPath := filepath.Join(destinationRoot, relativePath)
		if entry.IsDir() {
			return os.MkdirAll(destinationPath, entryInfo.Mode().Perm())
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(destinationPath, content, entryInfo.Mode().Perm())
	})
}

// initMarksmanWorkspaceGitRoot marks the temp fixture copy as one repository so Marksman enables project-wide links.
func initMarksmanWorkspaceGitRoot(t *testing.T, workspaceRoot string) error {
	t.Helper()

	command := exec.CommandContext(t.Context(), "git", "init", "-q")
	command.Dir = workspaceRoot
	output, err := command.CombinedOutput()
	if err != nil {
		return err
	}
	if len(output) != 0 {
		return nil
	}

	return nil
}

// writeMarksmanWorkspaceFile keeps live mutation tests focused on watcher behavior instead of repeated directory setup.
func writeMarksmanWorkspaceFile(t *testing.T, workspaceRoot, relativePath, content string) {
	t.Helper()

	absolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(relativePath))
	require.NoError(t, os.MkdirAll(filepath.Dir(absolutePath), 0o755))
	require.NoError(t, os.WriteFile(absolutePath, []byte(content), 0o600))
}

// assertOverviewContainsPath keeps overview assertions focused on the stable file/path pair.
func assertOverviewContainsPath(t *testing.T, symbols []domain.SymbolLocation, filePath, namePath string) {
	t.Helper()

	for _, symbol := range symbols {
		if symbol.File == filePath && symbol.Path == namePath {
			return
		}
	}

	t.Fatalf("expected symbol %q in %q, got %#v", namePath, filePath, symbols)
}

// findFoundSymbol keeps integration assertions readable when find_symbol returns a flat slice of matches.
func findFoundSymbol(symbols []domain.FoundSymbol, namePath string) (domain.FoundSymbol, bool) {
	for _, symbol := range symbols {
		if symbol.Path == namePath {
			return symbol, true
		}
	}

	return domain.FoundSymbol{}, false
}

// findReferencingSymbol keeps grouped-reference assertions short and focused on behavior.
func findReferencingSymbol(symbols []domain.ReferencingSymbol, namePath string) (domain.ReferencingSymbol, bool) {
	for _, symbol := range symbols {
		if symbol.Path == namePath {
			return symbol, true
		}
	}

	return domain.ReferencingSymbol{}, false
}
