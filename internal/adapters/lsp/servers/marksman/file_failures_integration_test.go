//go:build integration_tests

package lspmarksman

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/n-r-w/asteria/internal/domain"
)

// unreadableFileMode removes every permission so the next read of the file fails.
const unreadableFileMode = 0o000

// marksmanFileFailureSuite checks searches over a workspace where some Markdown files cannot be read.
type marksmanFileFailureSuite struct {
	suite.Suite
}

// TestIntegrationMarksmanFileFailures runs the Marksman searches over workspaces with unreadable files.
func TestIntegrationMarksmanFileFailures(t *testing.T) {
	suite.Run(t, new(marksmanFileFailureSuite))
}

// SetupTest skips platforms where a file without permissions stays readable.
func (s *marksmanFileFailureSuite) SetupTest() {
	if runtime.GOOS == "windows" {
		s.T().Skip("chmod does not make a file unreadable on Windows")
	}
	if os.Geteuid() == 0 {
		s.T().Skip("root reads files without permissions")
	}
}

// TestFindSymbolReturnsSymbolsOfOtherFilesAndNamesFailedFile checks that a workspace search keeps the symbols of
// readable files and names the unreadable file with its reason.
func (s *marksmanFileFailureSuite) TestFindSymbolReturnsSymbolsOfOtherFilesAndNamesFailedFile() {
	t := s.T()
	workspaceRoot := prepareMarksmanWorkspace(t)
	service := newIntegrationService(t)

	writeMarksmanWorkspaceFile(t, workspaceRoot, "docs/alpha.md", "# Search Target\n")
	writeMarksmanWorkspaceFile(t, workspaceRoot, "docs/beta.md", "# Search Target\n")
	writeMarksmanWorkspaceFile(t, workspaceRoot, "docs/broken.md", "# Search Target\n")
	startMarksmanSession(t, service, workspaceRoot)
	makeMarksmanWorkspaceFileUnreadable(t, workspaceRoot, "docs/broken.md")

	result, err := service.FindSymbol(t.Context(), &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "Search Target"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "",
	})
	s.Require().NoError(err)

	files := make([]string, 0, len(result.Symbols))
	for _, symbol := range result.Symbols {
		files = append(files, symbol.File)
	}
	s.ElementsMatch([]string{"docs/alpha.md", "docs/beta.md"}, files)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "docs/broken.md: ")
	s.Contains(result.Warnings[0], "permission denied")
}

// TestFindSymbolNamesThreeFailedFilesAndCountsTheRest checks that the warning names three failed files and gives
// only the count of the rest.
func (s *marksmanFileFailureSuite) TestFindSymbolNamesThreeFailedFilesAndCountsTheRest() {
	t := s.T()
	workspaceRoot := prepareMarksmanWorkspace(t)
	service := newIntegrationService(t)

	writeMarksmanWorkspaceFile(t, workspaceRoot, "docs/alpha.md", "# Search Target\n")
	brokenFiles := make([]string, 0, 5)
	for index := range 5 {
		brokenFile := fmt.Sprintf("docs/broken%d.md", index)
		writeMarksmanWorkspaceFile(t, workspaceRoot, brokenFile, "# Search Target\n")
		brokenFiles = append(brokenFiles, brokenFile)
	}
	startMarksmanSession(t, service, workspaceRoot)
	for _, brokenFile := range brokenFiles {
		makeMarksmanWorkspaceFileUnreadable(t, workspaceRoot, brokenFile)
	}

	result, err := service.FindSymbol(t.Context(), &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "Search Target"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "docs",
	})
	s.Require().NoError(err)

	s.Require().Len(result.Symbols, 1)
	s.Equal("docs/alpha.md", result.Symbols[0].File)
	s.Require().Len(result.Warnings, 1)
	namedFiles := 0
	for _, brokenFile := range brokenFiles {
		if strings.Contains(result.Warnings[0], brokenFile+": ") {
			namedFiles++
		}
	}
	s.Equal(3, namedFiles)
	s.Contains(result.Warnings[0], "2 more")
}

// TestFindReferencingSymbolsReturnsOtherReferencesAndNamesFailedFile checks that a reference search keeps the
// references of readable files and names the unreadable referencing file.
func (s *marksmanFileFailureSuite) TestFindReferencingSymbolsReturnsOtherReferencesAndNamesFailedFile() {
	t := s.T()
	workspaceRoot := prepareMarksmanWorkspace(t)
	service := newIntegrationService(t)

	writeMarksmanWorkspaceFile(
		t,
		workspaceRoot,
		"docs/other.md",
		"# Other\n\nSee the [installation guide](../guide.markdown#installation).\n",
	)
	request := &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "Guide/Installation"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "guide.markdown",
	}
	// The first search lets Marksman index README.md while the file is still readable.
	warmResult, err := service.FindReferencingSymbols(t.Context(), request)
	s.Require().NoError(err)
	_, ok := findReferencingSymbol(warmResult.Symbols, "Project Overview/Usage")
	s.Require().True(ok, "expected README.md reference before the file becomes unreadable, got %#v", warmResult.Symbols)
	makeMarksmanWorkspaceFileUnreadable(t, workspaceRoot, "README.md")

	result, err := service.FindReferencingSymbols(t.Context(), request)
	s.Require().NoError(err)

	otherSymbol, ok := findReferencingSymbol(result.Symbols, "Other")
	s.Require().True(ok, "expected docs/other.md reference, got %#v", result.Symbols)
	s.Equal("docs/other.md", otherSymbol.File)
	for _, symbol := range result.Symbols {
		s.NotEqual("README.md", symbol.File)
	}
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "README.md: ")
	s.Contains(result.Warnings[0], "permission denied")
}

// startMarksmanSession starts Marksman while each file is readable, because Marksman stops when it cannot load
// a workspace file at startup.
func startMarksmanSession(t *testing.T, service *Service, workspaceRoot string) {
	t.Helper()

	_, err := service.GetSymbolsOverview(t.Context(), &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 0},
		WorkspaceRoot:            workspaceRoot,
		File:                     "README.md",
	})
	require.NoError(t, err)
}

// makeMarksmanWorkspaceFileUnreadable removes read permission until the test ends.
func makeMarksmanWorkspaceFileUnreadable(t *testing.T, workspaceRoot, relativePath string) {
	t.Helper()

	absolutePath := filepath.Join(workspaceRoot, filepath.FromSlash(relativePath))
	info, err := os.Stat(absolutePath)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(absolutePath, unreadableFileMode))
	t.Cleanup(func() {
		_ = os.Chmod(absolutePath, info.Mode().Perm())
	})
}
