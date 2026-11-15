package lspphpactor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"go.lsp.dev/protocol"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/runtimelsp"
	"github.com/n-r-w/asteria/internal/domain"
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

// foundSymbol converts the constant into one find_symbol result with the body and info that the request asks for.
func (c *phpConstantSymbol) foundSymbol(request *domain.FindSymbolRequest) domain.FoundSymbol {
	foundSymbol := domain.FoundSymbol{
		Kind:      int(protocol.SymbolKindConstant),
		Body:      "",
		Info:      "",
		Path:      c.Name,
		File:      c.File,
		StartLine: c.StartLine,
		EndLine:   c.EndLine,
	}
	if request.IncludeBody {
		foundSymbol.Body = c.Declaration
	}
	if request.IncludeInfo {
		foundSymbol.Info = c.Declaration
	}

	return foundSymbol
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
// recordFileFailure takes the error of one file and returns it back only when the error must end the search.
// stopByTimeout takes the current file and the files after it and reports whether the scan must stop because the
// request deadline passed; the result then keeps the constants of the files before them.
func augmentFindSymbolWithPHPConstants(
	workspaceRoot string,
	request *domain.FindSymbolRequest,
	result domain.FindSymbolResult,
	recordFileFailure func(relativePath string, err error) error,
	stopByTimeout func(notSearched []string) bool,
) (domain.FindSymbolResult, error) {
	if !phpConstantFallbackApplies(request) {
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

	for fileIndex, relativePath := range searchFiles {
		if stopByTimeout(searchFiles[fileIndex:]) {
			break
		}
		constantSymbols, collectErr := collectPHPFileConstants(workspaceRoot, relativePath)
		if collectErr != nil {
			// The constants of the files finished before the stop stay in the result.
			if stopByTimeout(searchFiles[fileIndex:]) {
				break
			}
			if recordErr := recordFileFailure(relativePath, collectErr); recordErr != nil {
				return domain.FindSymbolResult{}, recordErr
			}

			continue
		}

		for _, constantSymbol := range constantSymbols {
			if !phpConstantPathMatches(request.Path, constantSymbol.Name, request.SubstringMatching) {
				continue
			}

			foundSymbol := constantSymbol.foundSymbol(request)
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

// phpConstantFallbackApplies reports whether the request can match a top-level constant, so that the constant
// fallback of find_symbol can add symbols to its result.
func phpConstantFallbackApplies(request *domain.FindSymbolRequest) bool {
	return phpConstantRequestCanMatch(request.Path) &&
		phpMatchesKindFilters(int(protocol.SymbolKindConstant), request.IncludeKinds, request.ExcludeKinds)
}

// augmentPropertyReferenceResults supplements the LSP reference result with phpactor's member-reference CLI
// output for PHP properties, which closes cases where the LSP transport returns no field usages.
// recordFileFailure takes the error of one referencing file and returns it back only when the error must end
// the search. stopByTimeout takes the files of the current CLI row and the rows after it and reports whether the
// fallback must stop because the request deadline passed; the result then keeps the rows before them.
func (s *Service) augmentPropertyReferenceResults(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindReferencingSymbolsRequest,
	result domain.FindReferencingSymbolsResult,
	recordFileFailure func(relativePath string, err error) error,
	stopByTimeout func(notProcessed []string) bool,
) (domain.FindReferencingSymbolsResult, error) {
	targetSymbol, ok, err := s.findFallbackPropertyTarget(ctx, request)
	if err != nil || !ok {
		return result, err
	}

	referenceTarget, propertyName, splitOK := phpactorPropertyReferenceTarget(&targetSymbol)
	if !splitOK {
		return result, nil
	}

	// The command reads the index that the language server session of the workspace builds and keeps current.
	indexPath, err := phpactorIndexerPath(s.cacheRoot, workspaceRoot)
	if err != nil {
		return result, err
	}
	referenceRows, err := s.runPropertyReferences(ctx, workspaceRoot, indexPath, referenceTarget, propertyName)
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
		recordFileFailure,
		stopByTimeout,
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
// declaration lines that should not appear in the final reference response. A referencing file that cannot be
// processed goes to recordFileFailure without its rows; the target file too, because its property is already resolved.
// When stopByTimeout reports a stop before a row or after a failed row, the rows finished before it stay.
func (s *Service) collectAugmentedPropertyReferences(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindReferencingSymbolsRequest,
	targetSymbol *domain.FoundSymbol,
	referenceRows []phpactorReferenceRow,
	existingSymbols map[string]struct{},
	recordFileFailure func(relativePath string, err error) error,
	stopByTimeout func(notProcessed []string) bool,
) ([]domain.ReferencingSymbol, error) {
	overviewCache := make(map[string][]domain.SymbolLocation)
	failedFiles := make(map[string]struct{})
	augmentedSymbols := make([]domain.ReferencingSymbol, 0)
	// The files of the remaining rows are listed only when the deadline has passed.
	stopRowsByTimeout := func(rowIndex int) bool {
		return domain.IsStoppedByTimeout(ctx) && stopByTimeout(phpactorReferenceRowFiles(referenceRows[rowIndex:]))
	}
	for rowIndex, referenceRow := range referenceRows {
		// Rows of a file whose symbols are already loaded do not call the language server, so the deadline is
		// checked before each row and not only after an error.
		if stopRowsByTimeout(rowIndex) {
			break
		}
		if referenceRow.File == targetSymbol.File && referenceRow.Line == targetSymbol.StartLine {
			continue
		}
		if _, failed := failedFiles[referenceRow.File]; failed {
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
			if stopRowsByTimeout(rowIndex) {
				break
			}
			if recordErr := recordFileFailure(referenceRow.File, err); recordErr != nil {
				return nil, recordErr
			}
			failedFiles[referenceRow.File] = struct{}{}

			continue
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

// phpactorReferenceRowFiles returns the file of each CLI reference row.
func phpactorReferenceRowFiles(referenceRows []phpactorReferenceRow) []string {
	files := make([]string, 0, len(referenceRows))
	for _, referenceRow := range referenceRows {
		files = append(files, referenceRow.File)
	}

	return files
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
		Path:              "/" + strings.TrimLeft(trimmedPath, "/"),
		IncludeKinds:      []int{int(protocol.SymbolKindProperty), int(protocol.SymbolKindField)},
		ExcludeKinds:      nil,
		Depth:             0,
		IncludeBody:       false,
		IncludeInfo:       false,
		SubstringMatching: false,
		WorkspaceRoot:     request.WorkspaceRoot,
		Scope:             request.File,
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
			Depth:         phpOverviewDepth,
			WorkspaceRoot: workspaceRoot,
			File:          referenceRow.File,
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
		if bestIndex == -1 || width < bestWidth ||
			(width == bestWidth && symbol.StartLine < symbols[bestIndex].StartLine) {
			bestIndex = index
			bestWidth = width
		}
	}
	if bestIndex == -1 {
		return domain.SymbolLocation{}, false
	}

	return symbols[bestIndex], true
}

// runPHPActorPropertyReferences executes phpactor's official member-reference command for one property target over
// the index at indexPath and parses its table output into file/line pairs.
func runPHPActorPropertyReferences(
	ctx context.Context,
	workspaceRoot string,
	indexPath string,
	referenceTarget string,
	propertyName string,
) ([]phpactorReferenceRow, error) {
	configExtra, err := json.Marshal(map[string]string{phpactorIndexerPathKey: indexPath})
	if err != nil {
		return nil, fmt.Errorf("encode phpactor index path: %w", err)
	}

	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	cmd := runtimelsp.NewCommand(
		ctx,
		phpactorServerName,
		"--no-interaction",
		"--no-ansi",
		"--config-extra="+string(configExtra),
		"references:member",
		"--type=property",
		referenceTarget,
		propertyName,
	)
	cmd.Dir = workspaceRoot
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err = cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("run phpactor property references: %w", ctxErr)
		}

		return nil, fmt.Errorf("run phpactor property references: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return parsePHPActorReferenceRows(stdout.String())
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
		return nil, domain.NewPathError("relative path", cleanScopePath, err)
	}
	if !fileInfo.IsDir() {
		if strings.ToLower(filepath.Ext(cleanScopePath)) != phpExtension {
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

// handleFallbackError returns a fallback error that ends the whole request, and otherwise keeps the optional
// fallback failure visible in logs without turning it into a user-facing error, because the primary LSP result
// is still usable.
func handleFallbackError(ctx context.Context, message string, err error, attrs ...any) error {
	if helpers.IsRequestError(ctx, err) {
		return err
	}

	logAttrs := append([]any{"err", err}, attrs...)
	slog.WarnContext(ctx, message, logAttrs...)

	return nil
}
