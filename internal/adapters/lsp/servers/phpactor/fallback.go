package lspphpactor

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"go.lsp.dev/protocol"
)

var phpTopLevelConstantPattern = regexp.MustCompile(`^\s*const\s+([A-Za-z_\x80-\xff][A-Za-z0-9_\x80-\xff]*)\b`)

const (
	phpOverviewDepth              = 8
	phpactorReferenceColumnCount  = 6
	phpConstantMatchSubmatchCount = 2
	phpPropertyPathMinComponents  = 2
)

// phpConstantSymbol stores one adapter-local top-level PHP constant that phpactor omits from documentSymbol.
type phpConstantSymbol struct {
	Name        string
	File        string
	StartLine   int
	EndLine     int
	Declaration string
}

// phpactorReferenceRow stores one parsed phpactor CLI reference line before it is grouped by container symbol.
type phpactorReferenceRow struct {
	File string
	Line int
}

// augmentOverviewWithPHPConstants supplements one-file overview results with top-level constants when phpactor's
// LSP document-symbol response omits them.
func augmentOverviewWithPHPConstants(
	workspaceRoot string,
	request *domain.GetSymbolsOverviewRequest,
	result domain.GetSymbolsOverviewResult,
) (domain.GetSymbolsOverviewResult, error) {
	constantSymbols, err := collectPHPFileConstants(workspaceRoot, request.File)
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}

	existingSymbols := make(map[string]struct{}, len(result.Symbols))
	for _, symbol := range result.Symbols {
		existingSymbols[phpOverviewSymbolKey(symbol)] = struct{}{}
	}

	for _, constantSymbol := range constantSymbols {
		overviewSymbol := domain.SymbolLocation{
			Kind:      int(protocol.SymbolKindConstant),
			Path:      constantSymbol.Name,
			File:      constantSymbol.File,
			StartLine: constantSymbol.StartLine,
			EndLine:   constantSymbol.EndLine,
		}
		if _, exists := existingSymbols[phpOverviewSymbolKey(overviewSymbol)]; exists {
			continue
		}

		result.Symbols = append(result.Symbols, overviewSymbol)
	}

	sort.Slice(result.Symbols, func(leftIndex, rightIndex int) bool {
		left := result.Symbols[leftIndex]
		right := result.Symbols[rightIndex]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.StartLine != right.StartLine {
			return left.StartLine < right.StartLine
		}
		if left.EndLine != right.EndLine {
			return left.EndLine < right.EndLine
		}

		return left.Path < right.Path
	})

	return result, nil
}

// augmentFindSymbolWithPHPConstants supplements standard find_symbol results with top-level constants when the
// requested scope includes PHP files that phpactor's document-symbol response does not cover.
func augmentFindSymbolWithPHPConstants(
	workspaceRoot string,
	request *domain.FindSymbolRequest,
	result domain.FindSymbolResult,
) (domain.FindSymbolResult, error) {
	if !phpConstantRequestCanMatch(request.Path) ||
		!phpMatchesKindFilters(int(protocol.SymbolKindConstant), request.IncludeKinds, request.ExcludeKinds) {
		return result, nil
	}

	searchFiles, err := collectPHPScopeFiles(workspaceRoot, request.Scope)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}

	existingSymbols := make(map[string]struct{}, len(result.Symbols))
	for _, symbol := range result.Symbols {
		existingSymbols[phpFoundSymbolKey(&symbol)] = struct{}{}
	}

	for _, relativePath := range searchFiles {
		constantSymbols, collectErr := collectPHPFileConstants(workspaceRoot, relativePath)
		if collectErr != nil {
			return domain.FindSymbolResult{}, collectErr
		}

		for _, constantSymbol := range constantSymbols {
			if !phpConstantPathMatches(request.Path, constantSymbol.Name, request.SubstringMatching) {
				continue
			}

			foundSymbol := domain.FoundSymbol{
				Kind:      int(protocol.SymbolKindConstant),
				Body:      "",
				Info:      "",
				Path:      constantSymbol.Name,
				File:      constantSymbol.File,
				StartLine: constantSymbol.StartLine,
				EndLine:   constantSymbol.EndLine,
			}
			if request.IncludeBody {
				foundSymbol.Body = constantSymbol.Declaration
			}
			if request.IncludeInfo {
				foundSymbol.Info = constantSymbol.Declaration
			}

			if _, exists := existingSymbols[phpFoundSymbolKey(&foundSymbol)]; exists {
				continue
			}

			result.Symbols = append(result.Symbols, foundSymbol)
		}
	}

	sort.Slice(result.Symbols, func(leftIndex, rightIndex int) bool {
		left := result.Symbols[leftIndex]
		right := result.Symbols[rightIndex]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.StartLine != right.StartLine {
			return left.StartLine < right.StartLine
		}
		if left.EndLine != right.EndLine {
			return left.EndLine < right.EndLine
		}

		return left.Path < right.Path
	})

	return result, nil
}

// augmentPropertyReferenceResults supplements the LSP reference result with phpactor's member-reference CLI
// output for PHP properties, which closes cases where the LSP transport returns no field usages.
func (s *Service) augmentPropertyReferenceResults(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindReferencingSymbolsRequest,
	result domain.FindReferencingSymbolsResult,
) (domain.FindReferencingSymbolsResult, error) {
	targetSymbol, ok, err := s.findFallbackPropertyTarget(ctx, request)
	if err != nil || !ok {
		return result, err
	}

	referenceTarget, propertyName, splitOK := phpactorPropertyReferenceTarget(&targetSymbol)
	if !splitOK {
		return result, nil
	}

	referenceRows, err := runPHPActorPropertyReferences(ctx, workspaceRoot, referenceTarget, propertyName)
	if err != nil {
		return result, err
	}
	if len(referenceRows) == 0 {
		return result, nil
	}

	existingSymbols := make(map[string]struct{}, len(result.Symbols))
	for _, symbol := range result.Symbols {
		existingSymbols[phpReferencingSymbolKey(symbol)] = struct{}{}
	}

	augmentedSymbols, err := s.collectAugmentedPropertyReferences(
		ctx,
		workspaceRoot,
		request,
		&targetSymbol,
		referenceRows,
		existingSymbols,
	)
	if err != nil {
		return result, err
	}
	result.Symbols = append(result.Symbols, augmentedSymbols...)

	sort.Slice(result.Symbols, func(leftIndex, rightIndex int) bool {
		left := result.Symbols[leftIndex]
		right := result.Symbols[rightIndex]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.ContentStartLine != right.ContentStartLine {
			return left.ContentStartLine < right.ContentStartLine
		}
		if left.ContentEndLine != right.ContentEndLine {
			return left.ContentEndLine < right.ContentEndLine
		}

		return left.Path < right.Path
	})

	return result, nil
}

// collectAugmentedPropertyReferences resolves CLI property-reference rows into grouped Asteria results and skips
// declaration lines that should not appear in the final reference response.
func (s *Service) collectAugmentedPropertyReferences(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindReferencingSymbolsRequest,
	targetSymbol *domain.FoundSymbol,
	referenceRows []phpactorReferenceRow,
	existingSymbols map[string]struct{},
) ([]domain.ReferencingSymbol, error) {
	overviewCache := make(map[string][]domain.SymbolLocation)
	augmentedSymbols := make([]domain.ReferencingSymbol, 0)
	for _, referenceRow := range referenceRows {
		if referenceRow.File == targetSymbol.File && referenceRow.Line == targetSymbol.StartLine {
			continue
		}

		containerSymbol, foundContainer, err := s.propertyReferenceContainer(
			ctx,
			workspaceRoot,
			request,
			referenceRow,
			overviewCache,
		)
		if err != nil {
			return nil, err
		}
		if !foundContainer {
			continue
		}
		if _, exists := existingSymbols[phpReferencingSymbolKey(containerSymbol)]; exists {
			continue
		}

		augmentedSymbols = append(augmentedSymbols, containerSymbol)
		existingSymbols[phpReferencingSymbolKey(containerSymbol)] = struct{}{}
	}

	return augmentedSymbols, nil
}

// findFallbackPropertyTarget resolves the exact property symbol that should be used for the phpactor
// member-reference fallback and rejects non-property targets.
func (s *Service) findFallbackPropertyTarget(
	ctx context.Context,
	request *domain.FindReferencingSymbolsRequest,
) (domain.FoundSymbol, bool, error) {
	trimmedPath := strings.TrimSpace(request.Path)
	if trimmedPath == "" {
		return domain.FoundSymbol{}, false, nil
	}

	result, err := s.std.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:              "/" + strings.TrimLeft(trimmedPath, "/"),
			IncludeKinds:      []int{int(protocol.SymbolKindProperty), int(protocol.SymbolKindField)},
			ExcludeKinds:      nil,
			Depth:             0,
			IncludeBody:       false,
			IncludeInfo:       false,
			SubstringMatching: false,
		},
		WorkspaceRoot: request.WorkspaceRoot,
		Scope:         request.File,
	})
	if err != nil {
		return domain.FoundSymbol{}, false, err
	}
	if len(result.Symbols) != 1 {
		return domain.FoundSymbol{}, false, nil
	}

	return result.Symbols[0], true, nil
}

// propertyReferenceContainer maps one phpactor CLI reference line back to the smallest containing symbol that
// should be exposed through Asteria's grouped reference contract.
func (s *Service) propertyReferenceContainer(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindReferencingSymbolsRequest,
	referenceRow phpactorReferenceRow,
	overviewCache map[string][]domain.SymbolLocation,
) (domain.ReferencingSymbol, bool, error) {
	symbols, ok := overviewCache[referenceRow.File]
	if !ok {
		overviewResult, err := s.std.GetSymbolsOverview(ctx, &domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: phpOverviewDepth},
			WorkspaceRoot:            workspaceRoot,
			File:                     referenceRow.File,
		})
		if err != nil {
			return domain.ReferencingSymbol{}, false, err
		}
		symbols = overviewResult.Symbols
		overviewCache[referenceRow.File] = symbols
	}

	container, foundContainer := smallestContainingSymbol(
		symbols,
		referenceRow.Line,
		request.IncludeKinds,
		request.ExcludeKinds,
	)
	if !foundContainer {
		return domain.ReferencingSymbol{}, false, nil
	}

	lineContent, err := readPHPFileLine(workspaceRoot, referenceRow.File, referenceRow.Line)
	if err != nil {
		return domain.ReferencingSymbol{}, false, err
	}

	return domain.ReferencingSymbol{
		Kind:             container.Kind,
		Path:             container.Path,
		File:             container.File,
		ContentStartLine: referenceRow.Line,
		ContentEndLine:   referenceRow.Line,
		Content:          lineContent,
	}, true, nil
}

// smallestContainingSymbol finds the narrowest symbol range that covers one 0-based reference line and passes
// the requested kind filters.
func smallestContainingSymbol(
	symbols []domain.SymbolLocation,
	line int,
	includeKinds []int,
	excludeKinds []int,
) (domain.SymbolLocation, bool) {
	bestWidth := -1
	bestIndex := -1
	for index, symbol := range symbols {
		if !phpMatchesKindFilters(symbol.Kind, includeKinds, excludeKinds) {
			continue
		}
		if line < symbol.StartLine || line > symbol.EndLine {
			continue
		}

		width := symbol.EndLine - symbol.StartLine
		if bestIndex == -1 || width < bestWidth || (width == bestWidth && symbol.StartLine < symbols[bestIndex].StartLine) {
			bestIndex = index
			bestWidth = width
		}
	}
	if bestIndex == -1 {
		return domain.SymbolLocation{}, false
	}

	return symbols[bestIndex], true
}

// runPHPActorPropertyReferences executes phpactor's official member-reference command for one property target and
// parses its table output into file/line pairs.
func runPHPActorPropertyReferences(
	ctx context.Context,
	workspaceRoot string,
	referenceTarget string,
	propertyName string,
) ([]phpactorReferenceRow, error) {
	if err := buildPHPActorIndex(ctx, workspaceRoot); err != nil {
		return nil, err
	}

	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	//nolint:gosec // Command and arguments are adapter-owned constants plus validated symbol names.
	cmd := exec.CommandContext(
		context.WithoutCancel(ctx),
		phpactorServerName,
		"--no-interaction",
		"--no-ansi",
		"references:member",
		"--type=property",
		referenceTarget,
		propertyName,
	)
	cmd.Dir = workspaceRoot
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run phpactor property references: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return parsePHPActorReferenceRows(stdout.String())
}

// buildPHPActorIndex prepares phpactor's project index before CLI fallback queries rely on class resolution.
func buildPHPActorIndex(ctx context.Context, workspaceRoot string) error {
	stderr := bytes.Buffer{}

	cmd := exec.CommandContext(
		context.WithoutCancel(ctx),
		phpactorServerName,
		"--no-interaction",
		"--no-ansi",
		"index:build",
	)
	cmd.Dir = workspaceRoot
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build phpactor index: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return nil
}

// parsePHPActorReferenceRows extracts file paths and 1-based line numbers from phpactor's tabular reference output.
func parsePHPActorReferenceRows(output string) ([]phpactorReferenceRow, error) {
	references := make([]phpactorReferenceRow, 0)
	seen := make(map[string]struct{})
	for rawLine := range strings.SplitSeq(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		trimmedLine := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(trimmedLine, "|") {
			continue
		}

		parts := strings.Split(rawLine, "|")
		if len(parts) < phpactorReferenceColumnCount {
			continue
		}

		referencePath := filepath.ToSlash(strings.TrimSpace(parts[1]))
		lineValue := strings.TrimSpace(parts[2])
		if referencePath == "" || referencePath == "Path" || lineValue == "" || lineValue == "LN" {
			continue
		}

		lineNumber, err := strconv.Atoi(lineValue)
		if err != nil {
			return nil, fmt.Errorf("parse phpactor reference line number %q: %w", lineValue, err)
		}

		reference := phpactorReferenceRow{File: referencePath, Line: lineNumber - 1}
		key := fmt.Sprintf("%s\x00%d", reference.File, reference.Line)
		if _, exists := seen[key]; exists {
			continue
		}

		references = append(references, reference)
		seen[key] = struct{}{}
	}

	return references, nil
}

// collectPHPScopeFiles resolves one optional file-or-directory scope into supported PHP files used by the
// constant fallback.
func collectPHPScopeFiles(workspaceRoot, scopePath string) ([]string, error) {
	trimmedScopePath := strings.TrimSpace(scopePath)
	if trimmedScopePath == "" || trimmedScopePath == "." {
		return helpers.CollectDirectoryFiles(workspaceRoot, workspaceRoot, extensions, shouldIgnoreDir)
	}

	cleanScopePath := filepath.Clean(trimmedScopePath)
	if filepath.IsAbs(cleanScopePath) {
		return nil, domain.NewPathMustBeWorkspaceRelativeError("relative path", scopePath)
	}

	absoluteScopePath := filepath.Join(workspaceRoot, cleanScopePath)
	relativeToRoot, err := filepath.Rel(workspaceRoot, absoluteScopePath)
	if err != nil {
		return nil, domain.NewInternalError(fmt.Errorf("resolve php scope %q: %w", scopePath, err))
	}
	if relativeToRoot == ".." || strings.HasPrefix(relativeToRoot, ".."+string(os.PathSeparator)) {
		return nil, domain.NewPathEscapesWorkspaceRootError("relative path", scopePath)
	}

	fileInfo, err := os.Stat(absoluteScopePath)
	if err != nil {
		return nil, domain.NewPathAccessError("relative path", cleanScopePath, err)
	}
	if !fileInfo.IsDir() {
		if strings.ToLower(filepath.Ext(cleanScopePath)) != ".php" {
			return nil, nil
		}

		return []string{cleanScopePath}, nil
	}

	return helpers.CollectDirectoryFiles(workspaceRoot, absoluteScopePath, extensions, shouldIgnoreDir)
}

// collectPHPFileConstants scans one PHP file for top-level `const` declarations so the adapter can restore
// symbols that phpactor omits from documentSymbol.
func collectPHPFileConstants(workspaceRoot, relativePath string) ([]phpConstantSymbol, error) {
	cleanRelativePath, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return nil, err
	}

	fileContent, err := os.ReadFile(filepath.Clean(absolutePath))
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.ReplaceAll(string(fileContent), "\r\n", "\n"), "\n")
	constants := make([]phpConstantSymbol, 0)
	braceDepth := 0
	for lineIndex, lineContent := range lines {
		if braceDepth == 0 {
			match := phpTopLevelConstantPattern.FindStringSubmatch(lineContent)
			if len(match) == phpConstantMatchSubmatchCount {
				constants = append(constants, phpConstantSymbol{
					Name:        match[1],
					File:        cleanRelativePath,
					StartLine:   lineIndex,
					EndLine:     lineIndex,
					Declaration: strings.TrimSpace(lineContent),
				})
			}
		}

		braceDepth += strings.Count(lineContent, "{")
		braceDepth -= strings.Count(lineContent, "}")
		if braceDepth < 0 {
			braceDepth = 0
		}
	}

	return constants, nil
}

// phpConstantRequestCanMatch quickly rejects search patterns that cannot match a top-level PHP constant path.
func phpConstantRequestCanMatch(path string) bool {
	trimmedPath := strings.Trim(strings.TrimSpace(path), "/")

	return trimmedPath != "" && !strings.Contains(trimmedPath, "/")
}

// phpConstantPathMatches applies the find_symbol absolute/suffix/substring rules to one top-level constant name.
func phpConstantPathMatches(pattern, constantPath string, substringMatching bool) bool {
	trimmedPattern := strings.TrimSpace(pattern)
	if trimmedPattern == "" {
		return false
	}

	isAbsolute := strings.HasPrefix(trimmedPattern, "/")
	needle := strings.Trim(trimmedPattern, "/")
	if needle == "" {
		return false
	}

	if substringMatching {
		return strings.Contains(constantPath, needle)
	}
	if isAbsolute {
		return constantPath == needle
	}

	return constantPath == needle
}

// phpMatchesKindFilters applies the reference/symbol kind filters locally for adapter-owned fallback results.
func phpMatchesKindFilters(kind int, includeKinds, excludeKinds []int) bool {
	if slices.Contains(excludeKinds, kind) {
		return false
	}
	if len(includeKinds) == 0 {
		return true
	}
	return slices.Contains(includeKinds, kind)
}

// phpactorPropertyReferenceTarget derives the phpactor member-reference target from the resolved declaration file
// and keeps the canonical property leaf for the second CLI argument.
func phpactorPropertyReferenceTarget(symbol *domain.FoundSymbol) (referenceTarget, propertyName string, ok bool) {
	if symbol == nil {
		return "", "", false
	}

	referenceTarget = strings.TrimSpace(symbol.File)
	components := strings.Split(strings.Trim(symbol.Path, "/"), "/")
	if len(components) < phpPropertyPathMinComponents {
		return "", "", false
	}

	propertyName = stripPHPPathDiscriminator(components[len(components)-1])
	if referenceTarget == "" || propertyName == "" {
		return "", "", false
	}

	return referenceTarget, propertyName, true
}

// stripPHPPathDiscriminator removes the exported duplicate-symbol suffix from one path component.
func stripPHPPathDiscriminator(component string) string {
	if before, _, ok := strings.Cut(component, "@"); ok {
		return before
	}

	return component
}

// readPHPFileLine returns one 0-based file line as the representative snippet for phpactor CLI references.
func readPHPFileLine(workspaceRoot, relativePath string, lineIndex int) (string, error) {
	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return "", err
	}

	fileContent, err := os.ReadFile(filepath.Clean(absolutePath))
	if err != nil {
		return "", err
	}

	lines := strings.Split(strings.ReplaceAll(string(fileContent), "\r\n", "\n"), "\n")
	if lineIndex < 0 || lineIndex >= len(lines) {
		return "", fmt.Errorf("line %d is out of range for %q", lineIndex, relativePath)
	}

	return lines[lineIndex], nil
}

// phpFoundSymbolKey builds one deterministic dedupe key for find_symbol results.
func phpOverviewSymbolKey(symbol domain.SymbolLocation) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d", symbol.File, symbol.Path, symbol.StartLine, symbol.EndLine)
}

// phpFoundSymbolKey builds one deterministic dedupe key for find_symbol results.
func phpFoundSymbolKey(symbol *domain.FoundSymbol) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d", symbol.File, symbol.Path, symbol.StartLine, symbol.EndLine)
}

// phpReferencingSymbolKey builds one deterministic dedupe key for grouped reference results.
func phpReferencingSymbolKey(symbol domain.ReferencingSymbol) string {
	return fmt.Sprintf("%s\x00%s", symbol.File, symbol.Path)
}

// logFallbackWarning keeps optional fallback failures visible in logs without turning them into user-facing
// errors when the primary LSP result is still usable.
func logFallbackWarning(ctx context.Context, message string, err error, attrs ...any) {
	logAttrs := append([]any{"err", err}, attrs...)
	slog.WarnContext(ctx, message, logAttrs...)
}
