package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"testing/synctest"
	"time"

	"github.com/n-r-w/asteria/internal/config"
	"github.com/n-r-w/asteria/internal/config/cfgadapters"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestToFoundSymbolDTOsKeepsFileForSingleFileScope keeps file information stable even for single-file search results.
func TestToFoundSymbolDTOsKeepsFileForSingleFileScope(t *testing.T) {
	t.Parallel()

	result := toFoundSymbolDTOs([]domain.FoundSymbol{{
		Kind:      12,
		Body:      "",
		Info:      "",
		Path:      "MakeBucket",
		File:      "internal/adapters/lsp/servers/gopls/testdata/basic/fixture.go",
		StartLine: 34,
		EndLine:   39,
	}})

	require.Len(t, result, 1)
	assert.Equal(t, "internal/adapters/lsp/servers/gopls/testdata/basic/fixture.go", result[0].File)
	assert.Equal(t, 12, result[0].Kind)
	assert.Equal(t, "MakeBucket", result[0].Path)
	assert.Equal(t, "34-39", result[0].Range)
}

// TestToFoundSymbolDTOsKeepsFileForMultiFileScope preserves file information when results may come from many files.
func TestToFoundSymbolDTOsKeepsFileForMultiFileScope(t *testing.T) {
	t.Parallel()

	result := toFoundSymbolDTOs([]domain.FoundSymbol{{
		Kind:      5,
		Body:      "",
		Info:      "",
		Path:      "Service",
		File:      "internal/usecase/router/service.go",
		StartLine: 18,
		EndLine:   23,
	}})

	require.Len(t, result, 1)
	assert.Equal(t, 5, result[0].Kind)
	assert.Equal(t, "internal/usecase/router/service.go", result[0].File)
}

// TestToReferencingFileDTOsGroupsByFile removes repeated file paths from per-symbol reference output.
func TestToReferencingFileDTOsGroupsByFile(t *testing.T) {
	t.Parallel()

	result := toReferencingFileDTOs([]domain.ReferencingSymbol{
		{
			Kind:             12,
			Path:             "UseMakeBucketTwice",
			File:             "internal/adapters/lsp/servers/gopls/testdata/basic/references.go",
			ContentStartLine: 3,
			ContentEndLine:   5,
			Content:          "func UseMakeBucketTwice(value string) string {\n\tleft := MakeBucket(value)\n\tright := MakeBucket(left.Describe())",
		},
		{
			Kind:             12,
			Path:             "UseMakeBucketOnce",
			File:             "internal/adapters/lsp/servers/gopls/testdata/basic/references.go",
			ContentStartLine: 11,
			ContentEndLine:   13,
			Content:          "func UseMakeBucketOnce(value string) FixtureBucket[string] {\n\treturn MakeBucket(value)\n}",
		},
	})

	require.Len(t, result, 1)
	assert.Equal(t, "internal/adapters/lsp/servers/gopls/testdata/basic/references.go", result[0].File)
	require.Len(t, result[0].Symbols, 2)
	assert.Equal(t, 12, result[0].Symbols[0].Kind)
	assert.Equal(t, "UseMakeBucketTwice", result[0].Symbols[0].Path)
	assert.Equal(t, 12, result[0].Symbols[1].Kind)
	assert.Equal(t, "UseMakeBucketOnce", result[0].Symbols[1].Path)
}

// TestToOverviewKindGroupDTOsGroupsByKind preserves line ranges while collapsing repeated kind values into one bucket.
func TestToOverviewKindGroupDTOsGroupsByKind(t *testing.T) {
	t.Parallel()

	result := toOverviewKindGroupDTOs([]domain.SymbolLocation{
		{Kind: 6, Path: "FixtureContract/Describe", File: "", StartLine: 14, EndLine: 14},
		{Kind: 12, Path: "MakeBucket", File: "", StartLine: 34, EndLine: 39},
		{Kind: 6, Path: "FixtureBucket/Describe", File: "", StartLine: 29, EndLine: 31},
	})

	require.Len(t, result, 2)
	assert.Equal(t, 6, result[0].Kind)
	assert.Equal(t, []overviewGroupSymbolDTO{
		{Path: "FixtureContract/Describe", Range: "14"},
		{Path: "FixtureBucket/Describe", Range: "29-31"},
	}, result[0].Symbols)
	assert.Equal(t, 12, result[1].Kind)
	assert.Equal(t, []overviewGroupSymbolDTO{{Path: "MakeBucket", Range: "34-39"}}, result[1].Symbols)
}

// TestGetSymbolsOverviewOutputJSONGroupsSymbolsByKind preserves grouped overview output while keeping zero-based line ranges.
func TestGetSymbolsOverviewOutputJSONGroupsSymbolsByKind(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(getSymbolsOverviewOutput{Groups: []overviewKindGroupDTO{{
		Kind: 23,
		Symbols: []overviewGroupSymbolDTO{{
			Path:  "Service",
			Range: "0",
		}},
	}}, ReturnedPercent: 0})
	require.NoError(t, err)
	assert.JSONEq(t, `{"groups":[{"kind":23,"symbols":[{"path":"Service","range":"0"}]}]}`, string(payload))
}

// TestFindSymbolOutputJSONKeepsKindRangeAndFile keeps file information stable in the published payload.
func TestFindSymbolOutputJSONKeepsKindRangeAndFile(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(findSymbolOutput{Symbols: toFoundSymbolDTOs([]domain.FoundSymbol{{
		Kind:      5,
		Body:      "",
		Info:      "",
		Path:      "Service",
		File:      "internal/server/service.go",
		StartLine: 0,
		EndLine:   0,
	}}), ReturnedPercent: 0})
	require.NoError(t, err)
	assert.JSONEq(t, `{"symbols":[{"kind":5,"path":"Service","file":"internal/server/service.go","range":"0"}]}`, string(payload))
}

// TestFindReferencingSymbolsOutputJSONKeepsEmptyFilesArray keeps no-hit results explicit instead of collapsing the payload shape.
func TestFindReferencingSymbolsOutputJSONKeepsEmptyFilesArray(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(findReferencingSymbolsOutput{Files: toReferencingFileDTOs(nil), ReturnedPercent: 0})
	require.NoError(t, err)
	assert.JSONEq(t, `{"files":[]}`, string(payload))
}

// TestFindReferencingSymbolsOutputJSONKeepsFlatReferenceFields preserves grouped symbol metadata in the published payload.
func TestFindReferencingSymbolsOutputJSONKeepsFlatReferenceFields(t *testing.T) {
	t.Parallel()

	payload, err := json.Marshal(findReferencingSymbolsOutput{Files: []referencingFileDTO{{
		File: "references.go",
		Symbols: []referencingSymbolDTO{{
			Kind:    12,
			Path:    "UseMakeBucketOnce",
			Range:   "0-2",
			Content: "",
		}},
	}}, ReturnedPercent: 0})
	require.NoError(t, err)
	assert.JSONEq(t, `{"files":[{"file":"references.go","symbols":[{"kind":12,"path":"UseMakeBucketOnce","range":"0-2","content":""}]}]}`, string(payload))
}

// TestProcessErrorKeepsSafeMessages proves that MCP tool responses keep only the public-safe message.
func TestProcessErrorKeepsSafeMessages(t *testing.T) {
	t.Parallel()

	err := processError(
		context.Background(),
		domain.ToolNameFindReferencingSymbols,
		domain.NewSafeError("use a more specific symbol_path", errors.New("internal detail")),
	)
	require.EqualError(t, err, "find_referencing_symbols: use a more specific symbol_path")
}

// TestSafeErrorLogLevelKeepsExpectedPublicFailuresOutOfErrorNoise proves that user-facing validation and
// routing messages do not masquerade as server failures in logs.
func TestSafeErrorLogLevelKeepsExpectedPublicFailuresOutOfErrorNoise(t *testing.T) {
	t.Parallel()

	assert.Equal(t, slog.LevelInfo, safeErrorLogLevel(domain.NewUnsupportedExtensionError(".md")))
}

// TestSafeErrorLogLevelKeepsInternalCauseAtErrorLevel proves that wrapped internal failures still surface as
// real errors even when the public message is sanitized.
func TestSafeErrorLogLevelKeepsInternalCauseAtErrorLevel(t *testing.T) {
	t.Parallel()

	assert.Equal(
		t,
		slog.LevelError,
		safeErrorLogLevel(domain.NewSafeError("internal error", errors.New("boom"))),
	)
}

// TestProcessErrorHidesUnexpectedInternalDetails proves that raw internal errors do not leak into MCP responses.
func TestProcessErrorHidesUnexpectedInternalDetails(t *testing.T) {
	t.Parallel()

	err := processError(context.Background(), domain.ToolNameFindSymbol, errors.New("boom *lspgopls.Service"))
	require.EqualError(t, err, "find_symbol: internal error")
}

// TestSanitizeValidationErrorUsesPublicArgumentNames keeps MCP validation feedback aligned with the public tool contract.
func TestSanitizeValidationErrorUsesPublicArgumentNames(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		toolName string
		err      error
		expected string
	}{
		{
			name:     "overview required file_path",
			toolName: domain.ToolNameGetSymbolsOverview,
			err:      errors.New("file is required"),
			expected: "file_path is required",
		},
		{
			name:     "overview required workspace_root",
			toolName: domain.ToolNameGetSymbolsOverview,
			err:      errors.New("workspace_root is required"),
			expected: "workspace_root is required",
		},
		{
			name:     "find_symbol required symbol_query",
			toolName: domain.ToolNameFindSymbol,
			err:      errors.New("path is required"),
			expected: "symbol_query is required",
		},
		{
			name:     "find_symbol required workspace_root",
			toolName: domain.ToolNameFindSymbol,
			err:      errors.New("workspace_root is required"),
			expected: "workspace_root is required",
		},
		{
			name:     "find_referencing required symbol_path",
			toolName: domain.ToolNameFindReferencingSymbols,
			err:      errors.New("path is required"),
			expected: "symbol_path is required",
		},
		{
			name:     "find_referencing required workspace_root",
			toolName: domain.ToolNameFindReferencingSymbols,
			err:      errors.New("workspace_root is required"),
			expected: "workspace_root is required",
		},
		{
			name:     "joined validation errors stay compact",
			toolName: domain.ToolNameFindSymbol,
			err: errors.Join(
				errors.New("path is required"),
				errors.New("depth must be non-negative"),
			),
			expected: "symbol_query is required; depth must be non-negative",
		},
		{
			name:     "invalid kinds preserve public field names",
			toolName: domain.ToolNameFindSymbol,
			err:      errors.New("include_kinds contains invalid symbol kind: 999"),
			expected: "include_kinds contains invalid symbol kind: 999",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			safeErr := sanitizeValidationError(testCase.toolName, testCase.err)
			require.EqualError(t, safeErr, testCase.expected)
		})
	}
}

// TestGetSymbolsOverviewToolMapsWorkspaceRoot proves that server DTO mapping forwards the selected root unchanged.
func TestGetSymbolsOverviewToolMapsWorkspaceRoot(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	search := NewMockILSP(ctrl)
	service := newToolTestService(search)

	input := &getSymbolsOverviewInput{
		WorkspaceRoot: "  /tmp/workspace  ",
		FilePath:      "  fixture.go  ",
		Depth:         1,
	}
	expectedRequest := &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: 1},
		WorkspaceRoot:            "/tmp/workspace",
		File:                     "fixture.go",
	}
	expectedResult := domain.GetSymbolsOverviewResult{Symbols: nil}

	search.EXPECT().GetSymbolsOverview(gomock.Any(), expectedRequest).Return(expectedResult, nil)

	_, output, err := service.getSymbolsOverviewTool(t.Context(), nil, input)
	require.NoError(t, err)
	assert.Empty(t, output.Groups)
}

// TestFindSymbolToolMapsWorkspaceRoot proves that find_symbol keeps workspace_root in the domain request.
func TestFindSymbolToolMapsWorkspaceRoot(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	search := NewMockILSP(ctrl)
	service := newToolTestService(search)

	input := &findSymbolInput{
		WorkspaceRoot:     "  /tmp/workspace  ",
		SymbolQuery:       "  Service  ",
		ScopePath:         "  internal/service.go  ",
		IncludeKinds:      []int{5},
		ExcludeKinds:      []int{6},
		Depth:             2,
		IncludeBody:       true,
		IncludeInfo:       false,
		SubstringMatching: true,
	}
	expectedRequest := &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:              "Service",
			IncludeKinds:      []int{5},
			ExcludeKinds:      []int{6},
			Depth:             2,
			IncludeBody:       true,
			IncludeInfo:       false,
			SubstringMatching: true,
		},
		WorkspaceRoot: "/tmp/workspace",
		Scope:         "internal/service.go",
	}
	expectedResult := domain.FindSymbolResult{Symbols: nil}

	search.EXPECT().FindSymbol(gomock.Any(), expectedRequest).Return(expectedResult, nil)

	_, output, err := service.findSymbolTool(t.Context(), nil, input)
	require.NoError(t, err)
	assert.Empty(t, output.Symbols)
}

// TestFindReferencingSymbolsToolMapsWorkspaceRoot proves that reference requests preserve workspace_root.
func TestFindReferencingSymbolsToolMapsWorkspaceRoot(t *testing.T) {
	t.Parallel()

	ctrl := gomock.NewController(t)
	search := NewMockILSP(ctrl)
	service := newToolTestService(search)

	input := &findReferencingSymbolsInput{
		WorkspaceRoot: "  /tmp/workspace  ",
		FilePath:      "  fixture.go  ",
		SymbolPath:    "  Service/Get  ",
		IncludeKinds:  []int{6},
		ExcludeKinds:  []int{12},
	}
	expectedRequest := &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path:         "Service/Get",
			IncludeKinds: []int{6},
			ExcludeKinds: []int{12},
		},
		WorkspaceRoot: "/tmp/workspace",
		File:          "fixture.go",
	}
	expectedResult := domain.FindReferencingSymbolsResult{Symbols: nil}

	search.EXPECT().FindReferencingSymbols(gomock.Any(), expectedRequest).Return(expectedResult, nil)

	_, output, err := service.findReferencingSymbolsTool(t.Context(), nil, input)
	require.NoError(t, err)
	assert.Empty(t, output.Files)
}

// TestFindReferencingSymbolsToolReturnsPublicTimeoutError proves that heavy tool calls stop at the configured server deadline.
func TestFindReferencingSymbolsToolReturnsPublicTimeoutError(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctrl := gomock.NewController(t)
		search := NewMockILSP(ctrl)
		service := newToolTestService(search)
		service.cfg.ToolTimeout = 10 * time.Second

		input := &findReferencingSymbolsInput{
			WorkspaceRoot: "/tmp/workspace",
			FilePath:      "fixture.go",
			SymbolPath:    "Service/Get",
			IncludeKinds:  nil,
			ExcludeKinds:  nil,
		}
		expectedRequest := &domain.FindReferencingSymbolsRequest{
			FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
				Path:         "Service/Get",
				IncludeKinds: nil,
				ExcludeKinds: nil,
			},
			WorkspaceRoot: "/tmp/workspace",
			File:          "fixture.go",
		}

		search.EXPECT().FindReferencingSymbols(gomock.Any(), expectedRequest).DoAndReturn(
			func(ctx context.Context, _ *domain.FindReferencingSymbolsRequest) (domain.FindReferencingSymbolsResult, error) {
				<-ctx.Done()
				return domain.FindReferencingSymbolsResult{}, ctx.Err()
			},
		)

		callCtx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()

		type toolCallResult struct {
			output findReferencingSymbolsOutput
			err    error
		}

		resultCh := make(chan toolCallResult, 1)
		go func() {
			_, output, err := service.findReferencingSymbolsTool(callCtx, nil, input)
			resultCh <- toolCallResult{output: output, err: err}
		}()

		result := <-resultCh
		require.EqualError(t, result.err, "find_referencing_symbols: tool execution timed out after 10s")
		assert.Empty(t, result.output.Files)
	})
}

// newToolTestService keeps DTO-mapping tests focused on request translation instead of repeated service setup.
func newToolTestService(search ILSP) *Service {
	return &Service{
		cfg: &config.Config{
			CacheRoot:              "",
			SystemPrompt:           "",
			GetSymbolsOverviewDesc: "",
			FindSymbolDesc:         "",
			FindReferencesDesc:     "",
			ToolTimeout:            10 * time.Second,
			ToolOutputMaxBytes:     0,
			Adapters:               cfgadapters.Config{},
		},
		mcpServer: nil,
		search:    search,
	}
}
