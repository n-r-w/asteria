package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/n-r-w/asteria/internal/domain"
)

// TestNewIgnoresDuplicateNormalizedExtensionsFromSameLSP proves that one LSP can
// advertise case-variant extensions without tripping the router duplicate check.
func TestNewIgnoresDuplicateNormalizedExtensionsFromSameLSP(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	clangdLSP := NewMockILSP(ctrl)
	clangdLSP.EXPECT().Extensions().Return([]string{".c", ".C", ".cpp", ".CPP"}).AnyTimes()

	svc, err := New([]ILSP{clangdLSP})
	require.NoError(t, err)
	require.NotNil(t, svc)
	assert.Len(t, svc.lspByExtension, 2)
	assert.Same(t, clangdLSP, svc.lspByExtension[".c"])
	assert.Same(t, clangdLSP, svc.lspByExtension[".cpp"])
}

// TestNewRejectsDuplicateNormalizedExtensionsAcrossDifferentLSPs proves that
// router startup still fails when two adapters compete for the same extension.
func TestNewRejectsDuplicateNormalizedExtensionsAcrossDifferentLSPs(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	firstLSP := NewMockILSP(ctrl)
	secondLSP := NewMockILSP(ctrl)
	firstLSP.EXPECT().Extensions().Return([]string{".c"}).AnyTimes()
	secondLSP.EXPECT().Extensions().Return([]string{".C"}).AnyTimes()

	_, err := New([]ILSP{firstLSP, secondLSP})
	require.Error(t, err)
	assert.ErrorContains(t, err, `multiple lsp implementations support extension ".C"`)
}

// TestServiceGetSymbolsOverview routes file-scoped requests through the matching LSP only.
func TestServiceGetSymbolsOverview(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()
	workspaceRoot = normalizeTestWorkspaceRoot(t, workspaceRoot)
	writeTestFile(t, workspaceRoot, "internal/usecase/router/service.go")

	ctrl := gomock.NewController(t)

	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)

	request := &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{
			Depth: 1,
		},
		WorkspaceRoot: workspaceRoot,
		File:          "internal/usecase/router/service.go",
	}
	lspRequest := &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{
			Depth: 1,
		},
		WorkspaceRoot: workspaceRoot,
		File:          "internal/usecase/router/service.go",
	}
	expected := domain.GetSymbolsOverviewResult{
		Symbols: []domain.SymbolLocation{{Kind: 1, Path: "Service", File: request.File, StartLine: 11, EndLine: 19}},
	}

	goLSP.EXPECT().GetSymbolsOverview(t.Context(), lspRequest).Return(expected, nil)

	result, getErr := svc.GetSymbolsOverview(t.Context(), request)
	require.NoError(t, getErr)
	assert.Equal(t, expected, result)
}

// TestServiceGetSymbolsOverviewRejectsUnsupportedExtension keeps file-only routing honest.
func TestServiceGetSymbolsOverviewRejectsUnsupportedExtension(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()
	writeTestFile(t, workspaceRoot, "README.md")

	ctrl := gomock.NewController(t)
	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)

	_, getErr := svc.GetSymbolsOverview(t.Context(),
		&domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 0},
			WorkspaceRoot:            workspaceRoot,
			File:                     "README.md",
		})
	require.Error(t, getErr)
	assert.ErrorContains(t, getErr, `files with extension ".md" are not supported`)
}

// TestServiceGetSymbolsOverviewRejectsMissingFileWithoutFilesystemNoise keeps user-facing errors free of syscall details.
func TestServiceGetSymbolsOverviewRejectsMissingFileWithoutFilesystemNoise(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()

	ctrl := gomock.NewController(t)
	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)

	_, getErr := svc.GetSymbolsOverview(t.Context(),
		&domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 0},
			WorkspaceRoot:            workspaceRoot,
			File:                     "missing.go",
		})
	require.Error(t, getErr)
	assert.EqualError(t, getErr, `file_path "missing.go" not found`)
}

// TestServiceGetSymbolsOverviewRejectsPathOutsideWorkspace keeps file requests inside the workspace root.
func TestServiceGetSymbolsOverviewRejectsPathOutsideWorkspace(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()

	ctrl := gomock.NewController(t)
	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)

	_, getErr := svc.GetSymbolsOverview(t.Context(),
		&domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 0},
			WorkspaceRoot:            workspaceRoot,
			File:                     "../outside.go",
		})
	require.Error(t, getErr)
	assert.ErrorContains(t, getErr, `escapes workspace root`)
}

// TestServiceGetSymbolsOverviewRejectsDirectoryPath keeps file-only tools from treating dotted directories as files.
func TestServiceGetSymbolsOverviewRejectsDirectoryPath(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceRoot, "pkg.v2"), 0o755))

	ctrl := gomock.NewController(t)
	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)

	_, getErr := svc.GetSymbolsOverview(t.Context(),
		&domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 0},
			WorkspaceRoot:            workspaceRoot,
			File:                     "pkg.v2",
		})
	require.Error(t, getErr)
	assert.ErrorContains(t, getErr, `points to a directory`)
}

// TestServiceFindSymbol routes file-scoped searches precisely and broad scopes by actual files under the path.
func TestServiceFindSymbol(t *testing.T) {
	t.Parallel()

	t.Run("file path", func(t *testing.T) {
		t.Parallel()
		workspaceRoot := t.TempDir()
		workspaceRoot = normalizeTestWorkspaceRoot(t, workspaceRoot)
		writeTestFile(t, workspaceRoot, "internal/usecase/router/service.go")

		ctrl := gomock.NewController(t)
		goLSP := NewMockILSP(ctrl)
		pyLSP := NewMockILSP(ctrl)
		goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()
		pyLSP.EXPECT().Extensions().Return([]string{".py"}).AnyTimes()

		svc, err := New([]ILSP{goLSP, pyLSP})
		require.NoError(t, err)

		request := &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:              "Service",
				Depth:             0,
				IncludeBody:       false,
				IncludeInfo:       false,
				IncludeKinds:      nil,
				ExcludeKinds:      nil,
				SubstringMatching: false,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "internal/usecase/router/service.go",
		}
		lspRequest := &domain.FindSymbolRequest{
			FindSymbolFilter: request.FindSymbolFilter,
			WorkspaceRoot:    workspaceRoot,
			Scope:            "internal/usecase/router/service.go",
		}
		expected := domain.FindSymbolResult{
			Symbols: []domain.FoundSymbol{{Kind: 23, Body: "", Info: "", Path: "Service", File: request.Scope, StartLine: 11, EndLine: 18}},
		}

		goLSP.EXPECT().FindSymbol(t.Context(), lspRequest).Return(expected, nil)

		result, findErr := svc.FindSymbol(t.Context(), request)
		require.NoError(t, findErr)
		assert.Equal(t, expected, result)
	})

	t.Run("directory scope only queries matching lsps", func(t *testing.T) {
		t.Parallel()
		workspaceRoot := t.TempDir()
		workspaceRoot = normalizeTestWorkspaceRoot(t, workspaceRoot)
		writeTestFile(t, workspaceRoot, "internal/usecase/router/service.go")

		ctrl := gomock.NewController(t)
		goLSP := NewMockILSP(ctrl)
		pyLSP := NewMockILSP(ctrl)
		goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()
		pyLSP.EXPECT().Extensions().Return([]string{".py"}).AnyTimes()

		svc, err := New([]ILSP{goLSP, pyLSP})
		require.NoError(t, err)

		request := &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:              "Service",
				Depth:             0,
				IncludeBody:       false,
				IncludeInfo:       false,
				IncludeKinds:      nil,
				ExcludeKinds:      nil,
				SubstringMatching: false,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "internal/usecase/router",
		}
		lspRequest := &domain.FindSymbolRequest{
			FindSymbolFilter: request.FindSymbolFilter,
			WorkspaceRoot:    workspaceRoot,
			Scope:            "internal/usecase/router",
		}
		expected := domain.FindSymbolResult{
			Symbols: []domain.FoundSymbol{
				{Kind: 23, Body: "", Info: "", Path: "Service", File: "internal/usecase/router/service.go", StartLine: 11, EndLine: 18},
				{Kind: 6, Body: "", Info: "", Path: "Service/GetSymbolsOverview", File: "internal/usecase/router/service.go", StartLine: 41, EndLine: 45},
			},
		}

		goLSP.EXPECT().FindSymbol(t.Context(), lspRequest).Return(expected, nil)

		result, findErr := svc.FindSymbol(t.Context(), request)
		require.NoError(t, findErr)
		assert.Equal(t, expected, result)
	})

	t.Run("directory with dot in name stays directory scope", func(t *testing.T) {
		t.Parallel()
		workspaceRoot := t.TempDir()
		workspaceRoot = normalizeTestWorkspaceRoot(t, workspaceRoot)
		writeTestFile(t, workspaceRoot, "pkg.v2/service.go")

		ctrl := gomock.NewController(t)
		goLSP := NewMockILSP(ctrl)
		goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

		svc, err := New([]ILSP{goLSP})
		require.NoError(t, err)

		request := &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:              "Service",
				Depth:             0,
				IncludeBody:       false,
				IncludeInfo:       false,
				IncludeKinds:      nil,
				ExcludeKinds:      nil,
				SubstringMatching: false,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "pkg.v2",
		}
		lspRequest := &domain.FindSymbolRequest{
			FindSymbolFilter: request.FindSymbolFilter,
			WorkspaceRoot:    workspaceRoot,
			Scope:            "pkg.v2",
		}
		expected := domain.FindSymbolResult{
			Symbols: []domain.FoundSymbol{{Kind: 23, Body: "", Info: "", Path: "Service", File: "pkg.v2/service.go", StartLine: 6, EndLine: 10}},
		}

		goLSP.EXPECT().FindSymbol(t.Context(), lspRequest).Return(expected, nil)

		result, findErr := svc.FindSymbol(t.Context(), request)
		require.NoError(t, findErr)
		assert.Equal(t, expected, result)
	})

	t.Run("path outside workspace is rejected", func(t *testing.T) {
		t.Parallel()
		workspaceRoot := t.TempDir()

		ctrl := gomock.NewController(t)
		goLSP := NewMockILSP(ctrl)
		goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

		svc, err := New([]ILSP{goLSP})
		require.NoError(t, err)

		_, findErr := svc.FindSymbol(t.Context(), &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:              "Service",
				Depth:             0,
				IncludeBody:       false,
				IncludeInfo:       false,
				IncludeKinds:      nil,
				ExcludeKinds:      nil,
				SubstringMatching: false,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "../outside",
		})
		require.Error(t, findErr)
		assert.ErrorContains(t, findErr, `escapes workspace root`)
	})

	t.Run("workspace scope merges matching lsps", func(t *testing.T) {
		t.Parallel()
		workspaceRoot := t.TempDir()
		workspaceRoot = normalizeTestWorkspaceRoot(t, workspaceRoot)
		writeTestFile(t, workspaceRoot, "internal/usecase/router/service.go")
		writeTestFile(t, workspaceRoot, "pkg/example.py")

		ctrl := gomock.NewController(t)
		goLSP := NewMockILSP(ctrl)
		pyLSP := NewMockILSP(ctrl)
		goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()
		pyLSP.EXPECT().Extensions().Return([]string{".py"}).AnyTimes()

		svc, err := New([]ILSP{goLSP, pyLSP})
		require.NoError(t, err)

		request := &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:              "Service",
				Depth:             0,
				IncludeBody:       false,
				IncludeInfo:       false,
				IncludeKinds:      nil,
				ExcludeKinds:      nil,
				SubstringMatching: false,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "",
		}
		goRequest := &domain.FindSymbolRequest{
			FindSymbolFilter: request.FindSymbolFilter,
			WorkspaceRoot:    workspaceRoot,
			Scope:            "",
		}
		pyRequest := &domain.FindSymbolRequest{
			FindSymbolFilter: request.FindSymbolFilter,
			WorkspaceRoot:    workspaceRoot,
			Scope:            "",
		}
		goResult := domain.FindSymbolResult{
			Symbols: []domain.FoundSymbol{
				{Kind: 23, Body: "", Info: "", Path: "Service", File: "internal/usecase/router/service.go", StartLine: 11, EndLine: 18},
				{Kind: 6, Body: "", Info: "", Path: "Service/GetSymbolsOverview", File: "internal/usecase/router/service.go", StartLine: 41, EndLine: 45},
			},
		}
		pyResult := domain.FindSymbolResult{
			Symbols: []domain.FoundSymbol{
				{Kind: 23, Body: "", Info: "python duplicate should be ignored", Path: "Service", File: "internal/usecase/router/service.go", StartLine: 11, EndLine: 18},
				{Kind: 23, Body: "", Info: "", Path: "Service", File: "pkg/example.py", StartLine: 6, EndLine: 9},
			},
		}

		goLSP.EXPECT().FindSymbol(t.Context(), goRequest).Return(goResult, nil)
		pyLSP.EXPECT().FindSymbol(t.Context(), pyRequest).Return(pyResult, nil)

		result, findErr := svc.FindSymbol(t.Context(), request)
		require.NoError(t, findErr)
		assert.ElementsMatch(t, []domain.FoundSymbol{
			{Kind: 23, Body: "", Info: "python duplicate should be ignored", Path: "Service", File: "internal/usecase/router/service.go", StartLine: 11, EndLine: 18},
			{Kind: 6, Body: "", Info: "", Path: "Service/GetSymbolsOverview", File: "internal/usecase/router/service.go", StartLine: 41, EndLine: 45},
			{Kind: 23, Body: "", Info: "", Path: "Service", File: "pkg/example.py", StartLine: 6, EndLine: 9},
		}, result.Symbols)
	})

	t.Run("workspace fan-out stops on lsp error", func(t *testing.T) {
		t.Parallel()
		workspaceRoot := t.TempDir()
		workspaceRoot = normalizeTestWorkspaceRoot(t, workspaceRoot)
		writeTestFile(t, workspaceRoot, "internal/usecase/router/service.go")
		writeTestFile(t, workspaceRoot, "pkg/example.py")

		ctrl := gomock.NewController(t)
		goLSP := NewMockILSP(ctrl)
		pyLSP := NewMockILSP(ctrl)
		goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()
		pyLSP.EXPECT().Extensions().Return([]string{".py"}).AnyTimes()

		svc, err := New([]ILSP{goLSP, pyLSP})
		require.NoError(t, err)

		request := &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:              "Service",
				Depth:             0,
				IncludeBody:       false,
				IncludeInfo:       false,
				IncludeKinds:      nil,
				ExcludeKinds:      nil,
				SubstringMatching: false,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "",
		}
		boom := errors.New("boom")
		lspRequest := &domain.FindSymbolRequest{
			FindSymbolFilter: request.FindSymbolFilter,
			WorkspaceRoot:    workspaceRoot,
			Scope:            "",
		}
		goLSP.EXPECT().FindSymbol(t.Context(), lspRequest).Return(domain.FindSymbolResult{}, boom).AnyTimes()
		pyLSP.EXPECT().FindSymbol(t.Context(), lspRequest).Return(domain.FindSymbolResult{}, boom).AnyTimes()

		_, findErr := svc.FindSymbol(t.Context(), request)
		require.Error(t, findErr)
		require.EqualError(t, findErr, "internal error")
	})

	t.Run("missing scope path is sanitized", func(t *testing.T) {
		t.Parallel()
		workspaceRoot := t.TempDir()

		ctrl := gomock.NewController(t)
		goLSP := NewMockILSP(ctrl)
		goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

		svc, err := New([]ILSP{goLSP})
		require.NoError(t, err)

		_, findErr := svc.FindSymbol(t.Context(), &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:              "Service",
				IncludeKinds:      nil,
				ExcludeKinds:      nil,
				Depth:             0,
				IncludeBody:       false,
				IncludeInfo:       false,
				SubstringMatching: false,
			},
			WorkspaceRoot: workspaceRoot,
			Scope:         "missing.go",
		})
		require.Error(t, findErr)
		assert.EqualError(t, findErr, `scope_path "missing.go" not found`)
	})
}

// TestServiceFindSymbolUsesExplicitWorkspaceRoot proves that router path resolution and adapter forwarding switch to the selected root.
func TestServiceFindSymbolUsesExplicitWorkspaceRoot(t *testing.T) {
	t.Parallel()

	selectedWorkspaceRoot := t.TempDir()
	writeTestFile(t, selectedWorkspaceRoot, "pkg/service.go")

	ctrl := gomock.NewController(t)
	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)
	normalizedSelectedWorkspaceRoot, normalizeErr := normalizeWorkspaceRoot(selectedWorkspaceRoot)
	require.NoError(t, normalizeErr)

	request := &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:              "Service",
			IncludeKinds:      nil,
			ExcludeKinds:      nil,
			Depth:             0,
			IncludeBody:       false,
			IncludeInfo:       false,
			SubstringMatching: false,
		},
		WorkspaceRoot: normalizedSelectedWorkspaceRoot,
		Scope:         "pkg/service.go",
	}
	lspRequest := &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:              "Service",
			IncludeKinds:      nil,
			ExcludeKinds:      nil,
			Depth:             0,
			IncludeBody:       false,
			IncludeInfo:       false,
			SubstringMatching: false,
		},
		WorkspaceRoot: normalizedSelectedWorkspaceRoot,
		Scope:         "pkg/service.go",
	}
	expected := domain.FindSymbolResult{
		Symbols: []domain.FoundSymbol{{Kind: 23, Body: "", Info: "", Path: "Service", File: "pkg/service.go", StartLine: 0, EndLine: 0}},
	}

	goLSP.EXPECT().FindSymbol(t.Context(), lspRequest).Return(expected, nil)

	result, findErr := svc.FindSymbol(t.Context(), request)
	require.NoError(t, findErr)
	assert.Equal(t, expected, result)
}

// TestServiceFindReferencingSymbols routes reference lookups through the file-specific LSP.
func TestServiceFindReferencingSymbols(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()
	workspaceRoot = normalizeTestWorkspaceRoot(t, workspaceRoot)
	writeTestFile(t, workspaceRoot, "internal/usecase/router/service.go")

	ctrl := gomock.NewController(t)
	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)

	request := &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path:         "Service/GetSymbolsOverview",
			IncludeKinds: nil,
			ExcludeKinds: nil,
		},
		WorkspaceRoot: workspaceRoot,
		File:          "internal/usecase/router/service.go",
	}
	lspRequest := &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path:         "Service/GetSymbolsOverview",
			IncludeKinds: nil,
			ExcludeKinds: nil,
		},
		WorkspaceRoot: workspaceRoot,
		File:          "internal/usecase/router/service.go",
	}

	expected := domain.FindReferencingSymbolsResult{
		Symbols: []domain.ReferencingSymbol{{
			Kind:             6,
			Path:             "Service/FindSymbol",
			File:             "internal/usecase/router/service.go",
			ContentStartLine: 48,
			ContentEndLine:   48,
			Content:          "GetSymbolsOverview",
		}},
	}

	goLSP.EXPECT().FindReferencingSymbols(t.Context(), lspRequest).Return(expected, nil)

	result, findErr := svc.FindReferencingSymbols(t.Context(), request)
	require.NoError(t, findErr)
	assert.Equal(t, expected, result)
}

// TestServiceGetSymbolsOverviewUsesExplicitWorkspaceRoot proves file-scoped overview routing uses the caller-selected root.
func TestServiceGetSymbolsOverviewUsesExplicitWorkspaceRoot(t *testing.T) {
	t.Parallel()

	selectedWorkspaceRoot := t.TempDir()
	writeTestFile(t, selectedWorkspaceRoot, "pkg/service.go")

	ctrl := gomock.NewController(t)
	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)
	normalizedSelectedWorkspaceRoot := normalizeTestWorkspaceRoot(t, selectedWorkspaceRoot)

	request := &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            normalizedSelectedWorkspaceRoot,
		File:                     "pkg/service.go",
	}
	lspRequest := &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            normalizedSelectedWorkspaceRoot,
		File:                     "pkg/service.go",
	}
	expected := domain.GetSymbolsOverviewResult{
		Symbols: []domain.SymbolLocation{{Kind: 23, Path: "Service", File: "pkg/service.go", StartLine: 0, EndLine: 0}},
	}

	goLSP.EXPECT().GetSymbolsOverview(t.Context(), lspRequest).Return(expected, nil)

	result, getErr := svc.GetSymbolsOverview(t.Context(), request)
	require.NoError(t, getErr)
	assert.Equal(t, expected, result)
}

// TestServiceGetSymbolsOverviewRejectsRelativeWorkspaceRoot keeps explicit root selection filesystem-safe.
func TestServiceGetSymbolsOverviewRejectsRelativeWorkspaceRoot(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)

	_, getErr := svc.GetSymbolsOverview(t.Context(), &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 0},
		WorkspaceRoot:            "relative-root",
		File:                     "fixture.go",
	})
	require.Error(t, getErr)
	assert.EqualError(t, getErr, `workspace_root "relative-root" must be absolute`)
}

// TestServiceRejectsEmptyWorkspaceRoot keeps router entrypoints aligned with the mandatory public contract.
func TestServiceRejectsEmptyWorkspaceRoot(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)

	t.Run("get symbols overview", func(t *testing.T) {
		t.Parallel()

		_, getErr := svc.GetSymbolsOverview(t.Context(), &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 0},
			WorkspaceRoot:            "",
			File:                     "fixture.go",
		})
		require.Error(t, getErr)
		assert.EqualError(t, getErr, "workspace_root is required")
	})

	t.Run("find symbol", func(t *testing.T) {
		t.Parallel()

		_, findErr := svc.FindSymbol(t.Context(), &domain.FindSymbolRequest{
			FindSymbolFilter: domain.FindSymbolFilter{
				Path:              "Service",
				IncludeKinds:      nil,
				ExcludeKinds:      nil,
				Depth:             0,
				IncludeBody:       false,
				IncludeInfo:       false,
				SubstringMatching: false,
			},
			WorkspaceRoot: "",
			Scope:         "fixture.go",
		})
		require.Error(t, findErr)
		assert.EqualError(t, findErr, "workspace_root is required")
	})

	t.Run("find referencing symbols", func(t *testing.T) {
		t.Parallel()

		_, findErr := svc.FindReferencingSymbols(t.Context(), &domain.FindReferencingSymbolsRequest{
			FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
				Path:         "Service/GetSymbolsOverview",
				IncludeKinds: nil,
				ExcludeKinds: nil,
			},
			WorkspaceRoot: "",
			File:          "fixture.go",
		})
		require.Error(t, findErr)
		assert.EqualError(t, findErr, "workspace_root is required")
	})
}

// TestServiceFindReferencingSymbolsRejectsDirectoryPath keeps target lookup file-scoped from the router entrypoint.
func TestServiceFindReferencingSymbolsRejectsDirectoryPath(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspaceRoot, "pkg.v2"), 0o755))

	ctrl := gomock.NewController(t)
	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)

	_, findErr := svc.FindReferencingSymbols(t.Context(), &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path:         "Service/GetSymbolsOverview",
			IncludeKinds: nil,
			ExcludeKinds: nil,
		},
		WorkspaceRoot: workspaceRoot,
		File:          "pkg.v2",
	})
	require.Error(t, findErr)
	assert.ErrorContains(t, findErr, `points to a directory`)
}

// TestServiceFindReferencingSymbolsUsesExplicitWorkspaceRoot proves reference routing uses the caller-selected root.
func TestServiceFindReferencingSymbolsUsesExplicitWorkspaceRoot(t *testing.T) {
	t.Parallel()

	selectedWorkspaceRoot := t.TempDir()
	writeTestFile(t, selectedWorkspaceRoot, "pkg/service.go")

	ctrl := gomock.NewController(t)
	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)
	normalizedSelectedWorkspaceRoot := normalizeTestWorkspaceRoot(t, selectedWorkspaceRoot)

	request := &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path:         "Service",
			IncludeKinds: nil,
			ExcludeKinds: nil,
		},
		WorkspaceRoot: normalizedSelectedWorkspaceRoot,
		File:          "pkg/service.go",
	}
	lspRequest := &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path:         "Service",
			IncludeKinds: nil,
			ExcludeKinds: nil,
		},
		WorkspaceRoot: normalizedSelectedWorkspaceRoot,
		File:          "pkg/service.go",
	}
	expected := domain.FindReferencingSymbolsResult{
		Symbols: []domain.ReferencingSymbol{{
			Kind:             12,
			Path:             "UseService",
			File:             "pkg/service.go",
			ContentStartLine: 1,
			ContentEndLine:   1,
			Content:          "Service()",
		}},
	}

	goLSP.EXPECT().FindReferencingSymbols(t.Context(), lspRequest).Return(expected, nil)

	result, findErr := svc.FindReferencingSymbols(t.Context(), request)
	require.NoError(t, findErr)
	assert.Equal(t, expected, result)
}

// TestServiceFindReferencingSymbolsRejectsUnsupportedExtensionWithoutLSPLeak keeps router details out of the published error.
func TestServiceFindReferencingSymbolsRejectsUnsupportedExtensionWithoutLSPLeak(t *testing.T) {
	t.Parallel()
	workspaceRoot := t.TempDir()
	writeTestFile(t, workspaceRoot, "AGENTS.md")

	ctrl := gomock.NewController(t)
	goLSP := NewMockILSP(ctrl)
	goLSP.EXPECT().Extensions().Return([]string{".go"}).AnyTimes()

	svc, err := New([]ILSP{goLSP})
	require.NoError(t, err)

	_, findErr := svc.FindReferencingSymbols(t.Context(), &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path:         "Anything",
			IncludeKinds: nil,
			ExcludeKinds: nil,
		},
		WorkspaceRoot: workspaceRoot,
		File:          "AGENTS.md",
	})
	require.Error(t, findErr)
	assert.EqualError(t, findErr, `files with extension ".md" are not supported`)
}

// TestSanitizePublicErrorReturnsTimeoutWarmupMessage keeps LSP startup timeouts out of the generic internal-error bucket.
func TestSanitizePublicErrorReturnsTimeoutWarmupMessage(t *testing.T) {
	t.Parallel()

	err := sanitizePublicError(
		fmt.Errorf("wait for rust-analyzer quiescent startup: %w", context.DeadlineExceeded),
		"find symbol in workspace",
	)

	require.EqualError(t, err, "request timed out, the LSP server may still be warming up")
}

// TestSanitizePublicErrorKeepsExplicitSafeMessages avoids replacing deliberate public messages with one generic timeout hint.
func TestSanitizePublicErrorKeepsExplicitSafeMessages(t *testing.T) {
	t.Parallel()

	err := sanitizePublicError(
		domain.NewSafeError(
			"project index is still loading, retry later",
			fmt.Errorf("wait for rust-analyzer quiescent startup: %w", context.DeadlineExceeded),
		),
		"find symbol in workspace",
	)

	require.EqualError(t, err, "project index is still loading, retry later")
}

func normalizeTestWorkspaceRoot(t *testing.T, workspaceRoot string) string {
	t.Helper()

	normalizedWorkspaceRoot, err := normalizeWorkspaceRoot(workspaceRoot)
	require.NoError(t, err)

	return normalizedWorkspaceRoot
}

// writeTestFile creates one workspace file so router tests can derive matching LSPs from real paths.
func writeTestFile(t *testing.T, workspaceRoot, relativePath string) {
	t.Helper()

	absPath := filepath.Join(workspaceRoot, relativePath)
	require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0o755))
	require.NoError(t, os.WriteFile(absPath, []byte("fixture\n"), 0o600))
}
