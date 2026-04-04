package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/n-r-w/asteria/internal/domain"
)

// getSymbolsOverviewTool handles get_symbols_overview requests.
func (s *Service) getSymbolsOverviewTool(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input *getSymbolsOverviewInput,
) (*mcp.CallToolResult, getSymbolsOverviewOutput, error) {
	if input == nil {
		return nil, getSymbolsOverviewOutput{},
			processError(ctx, domain.ToolNameGetSymbolsOverview, errors.New("input is nil"))
	}

	input.WorkspaceRoot = strings.TrimSpace(input.WorkspaceRoot)
	input.FilePath = strings.TrimSpace(input.FilePath)

	searchRequest := &domain.GetSymbolsOverviewRequest{
		GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{
			Depth: input.Depth,
		},
		WorkspaceRoot: input.WorkspaceRoot,
		File:          input.FilePath,
	}
	if err := searchRequest.Validate(); err != nil {
		return nil, getSymbolsOverviewOutput{}, processError(
			ctx,
			domain.ToolNameGetSymbolsOverview,
			sanitizeValidationError(domain.ToolNameGetSymbolsOverview, err),
		)
	}

	output, err := runWithToolTimeout(ctx, s, domain.ToolNameGetSymbolsOverview, func(
		ctx context.Context,
	) (getSymbolsOverviewOutput, error) {
		result, err := s.search.GetSymbolsOverview(ctx, searchRequest)
		if err != nil {
			return getSymbolsOverviewOutput{}, err
		}

		return s.limitGetSymbolsOverviewOutput(getSymbolsOverviewOutput{
			Groups:          toOverviewKindGroupDTOs(result.Symbols),
			ReturnedPercent: 0,
		})
	})
	if err != nil {
		return nil, getSymbolsOverviewOutput{}, err
	}

	return nil, output, nil
}

// findSymbolTool handles find_symbol requests.
func (s *Service) findSymbolTool(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input *findSymbolInput,
) (*mcp.CallToolResult, findSymbolOutput, error) {
	if input == nil {
		return nil, findSymbolOutput{}, processError(ctx, domain.ToolNameFindSymbol, errors.New("input is nil"))
	}

	input.WorkspaceRoot = strings.TrimSpace(input.WorkspaceRoot)
	input.SymbolQuery = strings.TrimSpace(input.SymbolQuery)
	input.ScopePath = strings.TrimSpace(input.ScopePath)

	searchRequest := &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:              input.SymbolQuery,
			Depth:             input.Depth,
			IncludeBody:       input.IncludeBody,
			IncludeInfo:       input.IncludeInfo,
			IncludeKinds:      input.IncludeKinds,
			ExcludeKinds:      input.ExcludeKinds,
			SubstringMatching: input.SubstringMatching,
		},
		WorkspaceRoot: input.WorkspaceRoot,
		Scope:         input.ScopePath,
	}
	if err := searchRequest.Validate(); err != nil {
		return nil, findSymbolOutput{}, processError(
			ctx,
			domain.ToolNameFindSymbol,
			sanitizeValidationError(domain.ToolNameFindSymbol, err),
		)
	}

	output, err := runWithToolTimeout(
		ctx,
		s,
		domain.ToolNameFindSymbol,
		func(ctx context.Context) (findSymbolOutput, error) {
			result, err := s.search.FindSymbol(ctx, searchRequest)
			if err != nil {
				return findSymbolOutput{}, err
			}

			return s.limitFindSymbolOutput(findSymbolOutput{
				Symbols:         toFoundSymbolDTOs(result.Symbols),
				ReturnedPercent: 0,
			})
		},
	)
	if err != nil {
		return nil, findSymbolOutput{}, err
	}

	return nil, output, nil
}

// findReferencingSymbolsTool handles find_referencing_symbols requests.
func (s *Service) findReferencingSymbolsTool(
	ctx context.Context,
	_ *mcp.CallToolRequest,
	input *findReferencingSymbolsInput,
) (*mcp.CallToolResult, findReferencingSymbolsOutput, error) {
	if input == nil {
		return nil, findReferencingSymbolsOutput{},
			processError(ctx, domain.ToolNameFindReferencingSymbols, errors.New("input is nil"))
	}

	input.WorkspaceRoot = strings.TrimSpace(input.WorkspaceRoot)
	input.FilePath = strings.TrimSpace(input.FilePath)
	input.SymbolPath = strings.TrimSpace(input.SymbolPath)

	searchRequest := &domain.FindReferencingSymbolsRequest{
		FindReferencingSymbolsFilter: domain.FindReferencingSymbolsFilter{
			Path:         input.SymbolPath,
			IncludeKinds: input.IncludeKinds,
			ExcludeKinds: input.ExcludeKinds,
		},
		WorkspaceRoot: input.WorkspaceRoot,
		File:          input.FilePath,
	}
	if err := searchRequest.Validate(); err != nil {
		return nil, findReferencingSymbolsOutput{}, processError(
			ctx,
			domain.ToolNameFindReferencingSymbols,
			sanitizeValidationError(domain.ToolNameFindReferencingSymbols, err),
		)
	}

	output, err := runWithToolTimeout(ctx, s, domain.ToolNameFindReferencingSymbols, func(
		ctx context.Context,
	) (findReferencingSymbolsOutput, error) {
		result, err := s.search.FindReferencingSymbols(ctx, searchRequest)
		if err != nil {
			return findReferencingSymbolsOutput{}, err
		}

		return s.limitFindReferencingSymbolsOutput(findReferencingSymbolsOutput{
			Files:           toReferencingFileDTOs(result.Symbols),
			ReturnedPercent: 0,
		})
	})
	if err != nil {
		return nil, findReferencingSymbolsOutput{}, err
	}

	return nil, output, nil
}

// runWithToolTimeout keeps one global execution deadline for every public MCP tool call.
func runWithToolTimeout[T any](
	ctx context.Context,
	s *Service,
	toolName string,
	run func(context.Context) (T, error),
) (T, error) {
	timeoutErr := domain.NewSafeError(
		fmt.Sprintf("tool execution timed out after %s", s.cfg.ToolTimeout),
		context.DeadlineExceeded,
	)
	timeoutCtx, cancel := context.WithTimeoutCause(ctx, s.cfg.ToolTimeout, timeoutErr)
	defer cancel()

	result, err := run(timeoutCtx)
	if err != nil {
		if errors.Is(context.Cause(timeoutCtx), timeoutErr) {
			return zeroValue[T](), processError(timeoutCtx, toolName, timeoutErr)
		}

		return zeroValue[T](), processError(timeoutCtx, toolName, err)
	}

	return result, nil
}

// toOverviewKindGroupDTOs groups overview symbols by kind while preserving per-kind symbol order.
func toOverviewKindGroupDTOs(input []domain.SymbolLocation) []overviewKindGroupDTO {
	out := make([]overviewKindGroupDTO, 0)
	groupIndexByKind := make(map[int]int)
	for _, symbol := range input {
		groupIndex, ok := groupIndexByKind[symbol.Kind]
		if !ok {
			out = append(out, overviewKindGroupDTO{
				Kind:    symbol.Kind,
				Symbols: make([]overviewGroupSymbolDTO, 0),
			})
			groupIndex = len(out) - 1
			groupIndexByKind[symbol.Kind] = groupIndex
		}

		out[groupIndex].Symbols = append(out[groupIndex].Symbols, overviewGroupSymbolDTO{
			Path:  symbol.Path,
			Range: formatRange(symbol.StartLine, symbol.EndLine),
		})
	}

	return out
}

// toFoundSymbolDTOs converts domain find_symbol results to transport DTO shape.
func toFoundSymbolDTOs(input []domain.FoundSymbol) []foundSymbolDTO {
	out := make([]foundSymbolDTO, 0, len(input))
	for _, symbol := range input {
		entry := foundSymbolDTO{
			Kind:  symbol.Kind,
			Body:  symbol.Body,
			Info:  symbol.Info,
			Path:  symbol.Path,
			File:  symbol.File,
			Range: formatRange(symbol.StartLine, symbol.EndLine),
		}
		out = append(out, entry)
	}

	return out
}

// toReferencingFileDTOs groups reference results by file before converting them to transport DTO shape.
func toReferencingFileDTOs(input []domain.ReferencingSymbol) []referencingFileDTO {
	if len(input) == 0 {
		return make([]referencingFileDTO, 0)
	}

	files := make([]referencingFileDTO, 0)
	currentFile := ""
	currentIndex := -1
	for _, symbol := range input {
		if currentIndex == -1 || currentFile != symbol.File {
			files = append(files, referencingFileDTO{
				File:    symbol.File,
				Symbols: make([]referencingSymbolDTO, 0),
			})
			currentIndex = len(files) - 1
			currentFile = symbol.File
		}

		files[currentIndex].Symbols = append(files[currentIndex].Symbols, referencingSymbolDTO{
			Kind:    symbol.Kind,
			Path:    symbol.Path,
			Range:   formatRange(symbol.ContentStartLine, symbol.ContentEndLine),
			Content: symbol.Content,
		})
	}

	return files
}

// formatRange converts one inclusive line range into the compact transport form expected by MCP clients.
func formatRange(startLine, endLine int) string {
	if startLine == endLine {
		return strconv.Itoa(startLine)
	}

	return fmt.Sprintf("%d-%d", startLine, endLine)
}

// processError logs the error with context and returns a formatted error for the tool response.
func processError(ctx context.Context, toolName string, err error) error {
	if err == nil {
		return nil
	}

	if safeErr, ok := errors.AsType[*domain.SafeError](err); ok {
		logLevel := safeErrorLogLevel(safeErr)
		cause := safeErr.Cause()
		if cause != nil {
			slog.Log(ctx, logLevel, toolName, "error", cause, "public_error", safeErr.Error())
		} else {
			slog.Log(ctx, logLevel, toolName, "error", safeErr.Error())
		}

		return fmt.Errorf("%s: %s", toolName, safeErr.Error())
	}

	slog.ErrorContext(ctx, toolName, "error", err)

	return fmt.Errorf("%s: internal error", toolName)
}

// safeErrorLogLevel keeps expected public validation and routing failures out of the error-level server noise.
func safeErrorLogLevel(safeErr *domain.SafeError) slog.Level {
	if safeErr == nil || safeErr.Cause() != nil {
		return slog.LevelError
	}

	return slog.LevelInfo
}

// sanitizeValidationError maps internal validation field names to the public MCP argument names.
func sanitizeValidationError(toolName string, err error) error {
	messages := flattenErrorMessages(err)
	if len(messages) == 0 {
		return domain.NewSafeError("invalid input", err)
	}

	publicMessages := make([]string, 0, len(messages))
	for _, message := range messages {
		publicMessages = append(publicMessages, mapValidationMessage(toolName, message))
	}

	return domain.NewSafeError(strings.Join(publicMessages, "; "), err)
}

// flattenErrorMessages converts wrapped and joined validation errors into one flat message list.
func flattenErrorMessages(err error) []string {
	if err == nil {
		return nil
	}

	type unwrapMany interface {
		Unwrap() []error
	}

	if many, ok := err.(unwrapMany); ok {
		messages := make([]string, 0)
		for _, childErr := range many.Unwrap() {
			messages = append(messages, flattenErrorMessages(childErr)...)
		}

		return messages
	}

	if childErr := errors.Unwrap(err); childErr != nil {
		return flattenErrorMessages(childErr)
	}

	return []string{err.Error()}
}

// mapValidationMessage rewrites internal validation field names into public MCP argument names.
func mapValidationMessage(toolName, message string) string {
	trimmedMessage := strings.TrimSpace(message)

	switch trimmedMessage {
	case "file is required":
		return "file_path is required"
	case "path is required":
		switch toolName {
		case domain.ToolNameFindSymbol:
			return "symbol_query is required"
		case domain.ToolNameFindReferencingSymbols:
			return "symbol_path is required"
		default:
			return trimmedMessage
		}
	default:
		return trimmedMessage
	}
}
