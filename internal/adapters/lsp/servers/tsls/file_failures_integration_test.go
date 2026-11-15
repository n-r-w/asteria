//go:build integration_tests

package lsptsls

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/n-r-w/asteria/internal/domain"
)

// tslsFileFailureSuite checks searches over a workspace where a TypeScript file cannot be read.
type tslsFileFailureSuite struct {
	suite.Suite
}

// TestIntegrationTSLSFileFailures runs the TypeScript searches over workspaces with an unreadable file.
func TestIntegrationTSLSFileFailures(t *testing.T) {
	suite.Run(t, new(tslsFileFailureSuite))
}

// SetupTest skips platforms where a file without permissions stays readable.
func (s *tslsFileFailureSuite) SetupTest() {
	if runtime.GOOS == "windows" {
		s.T().Skip("chmod does not make a file unreadable on Windows")
	}
	if os.Geteuid() == 0 {
		s.T().Skip("root reads files without permissions")
	}
}

// TestFindSymbolReturnsSymbolsOfOtherFilesAndNamesFailedFile checks that the re-export step skips the unreadable
// file and the warning names it once.
func (s *tslsFileFailureSuite) TestFindSymbolReturnsSymbolsOfOtherFilesAndNamesFailedFile() {
	t := s.T()
	workspaceRoot := copyTSLSFixtureRoot(t, "basic")
	service, ctx := newIntegrationService(t)

	brokenPath := filepath.Join(workspaceRoot, "broken.ts")
	require.NoError(t, os.WriteFile(brokenPath, []byte("export const jsHelper = 1;\n"), 0o600))
	_, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 0},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.js",
	})
	s.Require().NoError(err)
	s.Require().NoError(os.Chmod(brokenPath, 0o000))
	t.Cleanup(func() { _ = os.Chmod(brokenPath, 0o600) })

	result, err := service.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{Path: "jsHelper"},
		WorkspaceRoot:    workspaceRoot,
		Scope:            "",
	})
	s.Require().NoError(err)

	symbol, ok := findFoundSymbol(result.Symbols, "jsHelper")
	s.Require().True(ok, "expected whole-workspace match, got %#v", result.Symbols)
	s.Equal("fixture.js", symbol.File)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "broken.ts: ")
	s.Contains(result.Warnings[0], "permission denied")
}

// TestFindReferencingSymbolsSkipsUnreadableFileOfTargetDirectory checks that an unreadable file next to the target
// is skipped and named in the warning.
func (s *tslsFileFailureSuite) TestFindReferencingSymbolsSkipsUnreadableFileOfTargetDirectory() {
	t := s.T()
	workspaceRoot := copyTSLSFixtureRoot(t, "basic")
	service, ctx := newIntegrationService(t)

	_, err := service.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 0},
		WorkspaceRoot:            workspaceRoot,
		File:                     "fixture.ts",
	})
	s.Require().NoError(err)
	brokenPath := filepath.Join(workspaceRoot, "advanced.ts")
	s.Require().NoError(os.Chmod(brokenPath, 0o000))
	t.Cleanup(func() { _ = os.Chmod(brokenPath, 0o600) })

	result, err := service.FindReferencingSymbols(ctx, &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{Path: "makeBucket"},
		WorkspaceRoot:                workspaceRoot,
		File:                         "fixture.ts",
	})
	s.Require().NoError(err)

	symbol, ok := findReferencingSymbol(result.Symbols, "useBucket/right")
	s.Require().True(ok, "expected grouped references, got %#v", result.Symbols)
	s.Equal("references.ts", symbol.File)
	s.Require().Len(result.Warnings, 1)
	s.Contains(result.Warnings[0], "advanced.ts: ")
	s.Contains(result.Warnings[0], "permission denied")
}
