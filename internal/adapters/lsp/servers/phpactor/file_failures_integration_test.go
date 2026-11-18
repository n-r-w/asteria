//go:build integration_tests

package lspphpactor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/n-r-w/asteria/internal/domain"
)

// phpactorFileFailureSuite checks searches over a workspace where a PHP file cannot be read.
type phpactorFileFailureSuite struct {
	suite.Suite
}

// TestIntegrationPHPActorFileFailures runs the phpactor searches over workspaces with an unreadable file.
func TestIntegrationPHPActorFileFailures(t *testing.T) {
	suite.Run(t, new(phpactorFileFailureSuite))
}

// SetupTest skips platforms where a file without permissions stays readable.
func (s *phpactorFileFailureSuite) SetupTest() {
	if runtime.GOOS == "windows" {
		s.T().Skip("chmod does not make a file unreadable on Windows")
	}
	if os.Geteuid() == 0 {
		s.T().Skip("root reads files without permissions")
	}
}

// TestFindSymbolReturnsConstantsOfOtherFilesAndNamesFailedFile checks that the constant step skips the unreadable
// file and the warning names it once.
func (s *phpactorFileFailureSuite) TestFindSymbolReturnsConstantsOfOtherFilesAndNamesFailedFile() {
	t := s.T()
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	brokenPath := filepath.Join(workspaceRoot, "broken.php")
	require.NoError(t, os.WriteFile(brokenPath, []byte("<?php\n\nconst BROKEN_STAMP = 'broken';\n"), 0o600))
	_, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 0},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.php",
	})
	s.Require().NoError(err)
	s.Require().NoError(os.Chmod(brokenPath, 0o000))
	t.Cleanup(func() { _ = os.Chmod(brokenPath, 0o600) })

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "/FIXTURE_STAMP"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "",
	})
	s.Require().NoError(err)

	symbol, ok := findFoundSymbol(result.Symbols, "FIXTURE_STAMP")
	s.Require().True(ok, "expected constant match, got %#v", result.Symbols)
	s.Equal("fixture.php", symbol.File)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "1 file ")
	s.Contains(result.Warnings[0], "broken.php: ")
	s.Contains(result.Warnings[0], "permission denied")
}

// TestFindReferencingSymbolsIgnoresUnreadableFileOfTargetDirectory checks that an unreadable file next to the target
// neither stops the reference search nor appears in a warning, because the search opens only the target file.
func (s *phpactorFileFailureSuite) TestFindReferencingSymbolsIgnoresUnreadableFileOfTargetDirectory() {
	t := s.T()
	workspaceRoot := phpactorFixtureRoot(t)
	service, ctx := newIntegrationService(t)

	_, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 0},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.php",
	})
	s.Require().NoError(err)
	brokenPath := filepath.Join(workspaceRoot, "docblock.php")
	s.Require().NoError(os.Chmod(brokenPath, 0o000))
	t.Cleanup(func() { _ = os.Chmod(brokenPath, 0o600) })

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "Bucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.php",
	})
	s.Require().NoError(err)

	files := make(map[string]struct{}, len(result.Symbols))
	for _, symbol := range result.Symbols {
		files[symbol.File] = struct{}{}
	}
	s.Contains(files, "references.php")
	s.Empty(result.Warnings)
}
