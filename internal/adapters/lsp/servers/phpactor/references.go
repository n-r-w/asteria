package lspphpactor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"go.lsp.dev/protocol"
)

var (
	phpNamespacePattern = regexp.MustCompile(`(?m)^\s*namespace\s+([^;]+);`)
	phpUsePattern       = regexp.MustCompile(`(?m)^\s*use\s+([^;]+);`)
)

const phpReferenceTargetMinComponents = 2

// findReferencingSymbolsViaPHPActor routes PHP references through Phpactor's stable CLI commands when possible
// and falls back to narrow text scanning only for symbol kinds that Phpactor does not expose directly.
func (s *Service) findReferencingSymbolsViaPHPActor(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindReferencingSymbolsRequest,
) (domain.FindReferencingSymbolsResult, error) {
	targetSymbol, err := s.findExactReferenceTarget(ctx, request)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	referenceRows, skipFile, skipLine, err := s.referenceRowsForTarget(ctx, workspaceRoot, request, &targetSymbol)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	groupedReferences, err := s.collectReferenceRows(
		ctx,
		workspaceRoot,
		request,
		skipFile,
		skipLine,
		referenceRows,
		map[string]struct{}{},
	)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	sortReferencingSymbols(groupedReferences)

	return domain.FindReferencingSymbolsResult{Symbols: groupedReferences}, nil
}

// findExactReferenceTarget resolves the requested declaration symbol inside one file and preserves the same
// exact-match error behavior as the standard symbol-tree workflow.
func (s *Service) findExactReferenceTarget(
	ctx context.Context,
	request *domain.FindReferencingSymbolsRequest,
) (domain.FoundSymbol, error) {
	return s.findReferenceTargetByPath(ctx, request.WorkspaceRoot, request.File, request.Path)
}

// findReferenceTargetByPath reuses the adapter's exact find_symbol flow to resolve one declaration target.
func (s *Service) findReferenceTargetByPath(
	ctx context.Context,
	workspaceRoot string,
	file string,
	symbolPath string,
) (domain.FoundSymbol, error) {
	trimmedPath := strings.TrimSpace(symbolPath)
	if trimmedPath == "" {
		return domain.FoundSymbol{}, domain.NewSafeError(`no symbol matches ""`, nil)
	}

	result, err := s.FindSymbol(ctx, &domain.FindSymbolRequest{
		FindSymbolFilter: domain.FindSymbolFilter{
			Path:              "/" + strings.TrimLeft(trimmedPath, "/"),
			IncludeKinds:      nil,
			ExcludeKinds:      nil,
			Depth:             0,
			IncludeBody:       false,
			IncludeInfo:       false,
			SubstringMatching: false,
		},
		WorkspaceRoot: workspaceRoot,
		Scope:         file,
	})
	if err != nil {
		return domain.FoundSymbol{}, err
	}
	if len(result.Symbols) == 0 {
		return domain.FoundSymbol{}, domain.NewSafeError(fmt.Sprintf("no symbol matches %q", trimmedPath), nil)
	}
	if len(result.Symbols) == 1 {
		return result.Symbols[0], nil
	}

	trimmedExactPath := strings.TrimLeft(trimmedPath, "/")
	exactMatches := make([]domain.FoundSymbol, 0, len(result.Symbols))
	for _, candidate := range result.Symbols {
		if candidate.Path == trimmedExactPath {
			exactMatches = append(exactMatches, candidate)
		}
	}
	if len(exactMatches) == 1 {
		return exactMatches[0], nil
	}

	candidates := result.Symbols
	if len(exactMatches) > 1 {
		candidates = exactMatches
	}

	descriptions := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		descriptions = append(
			descriptions,
			fmt.Sprintf("%s (%s:%d)", candidate.Path, candidate.File, candidate.StartLine),
		)
	}
	sort.Strings(descriptions)

	return domain.FoundSymbol{}, domain.NewSafeError(
		fmt.Sprintf(
			"multiple symbols match %q; use a more specific symbol_path. Candidates: %s",
			trimmedPath,
			strings.Join(descriptions, ", "),
		),
		nil,
	)
}

// referenceRowsForTarget chooses the most stable Phpactor reference strategy for one resolved declaration symbol.
func (s *Service) referenceRowsForTarget(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindReferencingSymbolsRequest,
	targetSymbol *domain.FoundSymbol,
) (referenceRows []phpactorReferenceRow, skipFile string, skipLine int, err error) {
	switch targetSymbol.Kind {
	case int(protocol.SymbolKindProperty), int(protocol.SymbolKindField):
		var ok bool
		var propertyName string
		var referenceTarget string
		referenceTarget, propertyName, ok = phpactorPropertyReferenceTarget(targetSymbol)
		if !ok {
			return nil, targetSymbol.File, targetSymbol.StartLine, nil
		}

		referenceRows, err = runPHPActorPropertyReferences(
			ctx,
			s.cacheRoot,
			workspaceRoot,
			referenceTarget,
			propertyName,
		)

		return referenceRows, targetSymbol.File, targetSymbol.StartLine, err
	case int(protocol.SymbolKindMethod):
		var ok bool
		var memberName string
		var referenceTarget string
		referenceTarget, memberName, ok, err = phpactorMemberReferenceTarget(workspaceRoot, targetSymbol)
		if err != nil || !ok {
			return nil, targetSymbol.File, targetSymbol.StartLine, err
		}

		var runErr error
		referenceRows, runErr = runPHPActorMemberReferences(
			ctx,
			s.cacheRoot,
			workspaceRoot,
			"method",
			referenceTarget,
			memberName,
		)

		return referenceRows, targetSymbol.File, targetSymbol.StartLine, runErr
	case int(protocol.SymbolKindConstant):
		if phpIsTopLevelSymbol(targetSymbol.Path) {
			referenceRows, err = collectPHPPatternReferenceRows(
				workspaceRoot,
				regexp.MustCompile(`\b`+regexp.QuoteMeta(stripPHPPathDiscriminator(lastPHPPathComponent(targetSymbol.Path)))+`\b`),
			)

			return referenceRows, targetSymbol.File, targetSymbol.StartLine, err
		}

		var ok bool
		var memberName string
		var referenceTarget string
		referenceTarget, memberName, ok, err = phpactorMemberReferenceTarget(workspaceRoot, targetSymbol)
		if err != nil || !ok {
			return nil, targetSymbol.File, targetSymbol.StartLine, err
		}

		var runErr error
		referenceRows, runErr = runPHPActorMemberReferences(
			ctx,
			s.cacheRoot,
			workspaceRoot,
			"constant",
			referenceTarget,
			memberName,
		)

		return referenceRows, targetSymbol.File, targetSymbol.StartLine, runErr
	case int(protocol.SymbolKindConstructor), int(protocol.SymbolKindClass):
		return s.classReferenceRowsForTarget(ctx, workspaceRoot, request, targetSymbol)
	case int(protocol.SymbolKindFunction):
		leafName := stripPHPPathDiscriminator(lastPHPPathComponent(targetSymbol.Path))
		if leafName == "" {
			return nil, targetSymbol.File, targetSymbol.StartLine, nil
		}

		referenceRows, err = collectPHPPatternReferenceRows(
			workspaceRoot,
			regexp.MustCompile(`\b`+regexp.QuoteMeta(leafName)+`\s*\(`),
		)

		return referenceRows, targetSymbol.File, targetSymbol.StartLine, err
	default:
		leafName := stripPHPPathDiscriminator(lastPHPPathComponent(targetSymbol.Path))
		if leafName == "" {
			return nil, targetSymbol.File, targetSymbol.StartLine, nil
		}

		referenceRows, err = collectPHPPatternReferenceRows(
			workspaceRoot,
			regexp.MustCompile(`\b`+regexp.QuoteMeta(leafName)+`\b`),
		)

		return referenceRows, targetSymbol.File, targetSymbol.StartLine, err
	}
}

// classReferenceRowsForTarget uses Phpactor's class-reference CLI when it can identify the right declaration and
// falls back to alias-aware text scanning when Phpactor cannot address multi-class files.
func (s *Service) classReferenceRowsForTarget(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindReferencingSymbolsRequest,
	targetSymbol *domain.FoundSymbol,
) (referenceRows []phpactorReferenceRow, skipFile string, skipLine int, err error) {
	classSymbol := targetSymbol
	if targetSymbol.Kind == int(protocol.SymbolKindConstructor) {
		parentPath, ok := phpParentPath(targetSymbol.Path)
		if !ok {
			return nil, targetSymbol.File, targetSymbol.StartLine, nil
		}

		var resolvedClassSymbol domain.FoundSymbol
		resolvedClassSymbol, err = s.findReferenceTargetByPath(ctx, request.WorkspaceRoot, request.File, parentPath)
		if err != nil {
			return nil, targetSymbol.File, targetSymbol.StartLine, err
		}
		classSymbol = &resolvedClassSymbol
	}

	referenceTarget, err := phpactorClassReferenceTarget(workspaceRoot, classSymbol)
	if err != nil {
		return nil, classSymbol.File, classSymbol.StartLine, err
	}

	referenceRows, runErr := runPHPActorClassReferences(ctx, s.cacheRoot, workspaceRoot, referenceTarget)
	if runErr == nil && phpactorRowsContain(referenceRows, classSymbol.File, classSymbol.StartLine) {
		return referenceRows, classSymbol.File, classSymbol.StartLine, nil
	}

	textRows, textErr := collectPHPClassReferenceRows(workspaceRoot, classSymbol)
	if textErr != nil {
		if runErr != nil {
			return nil, classSymbol.File, classSymbol.StartLine, fmt.Errorf("%w; %w", runErr, textErr)
		}

		return nil, classSymbol.File, classSymbol.StartLine, textErr
	}

	return textRows, classSymbol.File, classSymbol.StartLine, nil
}

// runPHPActorClassReferences executes Phpactor's class-reference CLI command against one class target.
func runPHPActorClassReferences(
	ctx context.Context,
	cacheRoot string,
	workspaceRoot string,
	referenceTarget string,
) ([]phpactorReferenceRow, error) {
	indexPath, err := phpactorIndexerPath(cacheRoot, workspaceRoot)
	if err != nil {
		return nil, err
	}
	configExtra, err := phpactorConfigExtra(indexPath)
	if err != nil {
		return nil, err
	}

	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	//nolint:gosec // Command and arguments are adapter-owned values derived from validated PHP symbol paths.
	cmd := exec.CommandContext(
		context.WithoutCancel(ctx),
		phpactorServerName,
		"--no-interaction",
		"--no-ansi",
		"--config-extra",
		configExtra,
		"references:class",
		referenceTarget,
	)
	cmd.Dir = workspaceRoot
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr != nil {
		return nil, fmt.Errorf("run phpactor class references: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}

	return parsePHPActorReferenceRows(stdout.String())
}

// runPHPActorMemberReferences executes Phpactor's member-reference CLI for one resolved class/member pair.
func runPHPActorMemberReferences(
	ctx context.Context,
	cacheRoot string,
	workspaceRoot string,
	memberType string,
	referenceTarget string,
	memberName string,
) ([]phpactorReferenceRow, error) {
	indexPath, err := phpactorIndexerPath(cacheRoot, workspaceRoot)
	if err != nil {
		return nil, err
	}
	configExtra, err := phpactorConfigExtra(indexPath)
	if err != nil {
		return nil, err
	}

	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}

	//nolint:gosec // Command and arguments are adapter-owned values derived from validated PHP symbol paths.
	cmd := exec.CommandContext(
		context.WithoutCancel(ctx),
		phpactorServerName,
		"--no-interaction",
		"--no-ansi",
		"--config-extra",
		configExtra,
		"references:member",
		"--type="+memberType,
		referenceTarget,
		memberName,
	)
	cmd.Dir = workspaceRoot
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	if runErr != nil {
		return nil, fmt.Errorf("run phpactor %s references: %w: %s", memberType, runErr, strings.TrimSpace(stderr.String()))
	}

	return parsePHPActorReferenceRows(stdout.String())
}

// collectPHPPatternReferenceRows scans supported PHP files line-by-line for one narrow regex pattern.
func collectPHPPatternReferenceRows(workspaceRoot string, linePattern *regexp.Regexp) ([]phpactorReferenceRow, error) {
	return collectPHPTextReferenceRows(workspaceRoot, func(_ string, _ string, lines []string) ([]int, error) {
		matches := make([]int, 0)
		for index, line := range lines {
			if linePattern.MatchString(line) {
				matches = append(matches, index)
			}
		}

		return matches, nil
	})
}

// collectPHPClassReferenceRows scans PHP files for one class name plus any imported aliases that point to the
// same FQCN, which keeps class references usable when Phpactor cannot address multi-class files directly.
func collectPHPClassReferenceRows(workspaceRoot string, symbol *domain.FoundSymbol) ([]phpactorReferenceRow, error) {
	className := stripPHPPathDiscriminator(lastPHPPathComponent(symbol.Path))
	if className == "" {
		return nil, nil
	}

	classFQCN, err := phpQualifiedClassName(workspaceRoot, symbol)
	if err != nil {
		return nil, err
	}

	return collectPHPTextReferenceRows(workspaceRoot, func(_ string, content string, lines []string) ([]int, error) {
		candidateNames := append([]string{className}, phpImportedAliases(content, classFQCN)...)
		linePattern, buildErr := phpWordAlternationPattern(candidateNames)
		if buildErr != nil {
			return nil, buildErr
		}

		matches := make([]int, 0)
		for index, line := range lines {
			if linePattern.MatchString(line) {
				matches = append(matches, index)
			}
		}

		return matches, nil
	})
}

// collectPHPTextReferenceRows applies one matcher to every supported PHP file in the workspace.
func collectPHPTextReferenceRows(
	workspaceRoot string,
	matcher func(relativePath string, content string, lines []string) ([]int, error),
) ([]phpactorReferenceRow, error) {
	searchFiles, err := collectPHPScopeFiles(workspaceRoot, "")
	if err != nil {
		return nil, err
	}

	rows := make([]phpactorReferenceRow, 0)
	seen := make(map[string]struct{})
	for _, relativePath := range searchFiles {
		_, absolutePath, pathErr := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
		if pathErr != nil {
			return nil, pathErr
		}

		fileContent, readErr := os.ReadFile(filepath.Clean(absolutePath))
		if readErr != nil {
			return nil, readErr
		}

		normalizedContent := strings.ReplaceAll(string(fileContent), "\r\n", "\n")
		lineMatches, matchErr := matcher(relativePath, normalizedContent, strings.Split(normalizedContent, "\n"))
		if matchErr != nil {
			return nil, matchErr
		}

		for _, lineIndex := range lineMatches {
			key := fmt.Sprintf("%s\x00%d", relativePath, lineIndex)
			if _, ok := seen[key]; ok {
				continue
			}

			rows = append(rows, phpactorReferenceRow{File: relativePath, Line: lineIndex})
			seen[key] = struct{}{}
		}
	}

	return rows, nil
}

// phpactorMemberReferenceTarget derives the class target and member name that Phpactor expects for one class
// method or class-constant reference request.
func phpactorMemberReferenceTarget(
	workspaceRoot string,
	symbol *domain.FoundSymbol,
) (referenceTarget, memberName string, ok bool, err error) {
	if symbol == nil {
		return "", "", false, nil
	}

	components := strings.Split(strings.Trim(symbol.Path, "/"), "/")
	if len(components) < phpReferenceTargetMinComponents {
		return "", "", false, nil
	}

	memberName = stripPHPPathDiscriminator(components[len(components)-1])
	className := stripPHPPathDiscriminator(components[len(components)-2])
	if memberName == "" || className == "" {
		return "", "", false, nil
	}

	namespaceName, namespaceErr := phpNamespaceForFile(workspaceRoot, symbol.File)
	if namespaceErr != nil {
		return "", "", false, namespaceErr
	}
	if namespaceName == "" {
		return className, memberName, true, nil
	}

	return namespaceName + `\` + className, memberName, true, nil
}

// phpactorClassReferenceTarget chooses the most specific class target string that Phpactor's class-reference CLI
// can accept for one declaration.
func phpactorClassReferenceTarget(workspaceRoot string, symbol *domain.FoundSymbol) (string, error) {
	if symbol == nil {
		return "", nil
	}

	className := stripPHPPathDiscriminator(lastPHPPathComponent(symbol.Path))
	if className == "" {
		return "", nil
	}

	namespaceName, err := phpNamespaceForFile(workspaceRoot, symbol.File)
	if err != nil {
		return "", err
	}
	if namespaceName == "" {
		return strings.TrimSpace(symbol.File), nil
	}

	return namespaceName + `\` + className, nil
}

// phpQualifiedClassName builds one FQCN for alias-aware text matching.
func phpQualifiedClassName(workspaceRoot string, symbol *domain.FoundSymbol) (string, error) {
	if symbol == nil {
		return "", nil
	}

	className := stripPHPPathDiscriminator(lastPHPPathComponent(symbol.Path))
	if className == "" {
		return "", nil
	}

	namespaceName, err := phpNamespaceForFile(workspaceRoot, symbol.File)
	if err != nil {
		return "", err
	}
	if namespaceName == "" {
		return className, nil
	}

	return namespaceName + `\` + className, nil
}

// phpNamespaceForFile extracts one namespace declaration from a PHP source file.
func phpNamespaceForFile(workspaceRoot, relativePath string) (string, error) {
	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return "", err
	}

	fileContent, err := os.ReadFile(filepath.Clean(absolutePath))
	if err != nil {
		return "", err
	}

	match := phpNamespacePattern.FindStringSubmatch(string(fileContent))
	if len(match) != phpReferenceTargetMinComponents {
		return "", nil
	}

	return strings.TrimSpace(match[1]), nil
}

// phpImportedAliases collects the local alias names that import one target FQCN inside a PHP file.
func phpImportedAliases(fileContent, fqcn string) []string {
	normalizedFQCN := strings.TrimLeft(strings.TrimSpace(fqcn), `\`)
	if normalizedFQCN == "" {
		return nil
	}

	aliases := make([]string, 0)
	seen := make(map[string]struct{})
	for _, match := range phpUsePattern.FindAllStringSubmatch(fileContent, -1) {
		if len(match) != phpReferenceTargetMinComponents {
			continue
		}

		importPath, aliasName, ok := parsePHPUseStatement(match[1])
		if !ok || !strings.EqualFold(importPath, normalizedFQCN) || aliasName == "" {
			continue
		}
		if _, exists := seen[aliasName]; exists {
			continue
		}

		aliases = append(aliases, aliasName)
		seen[aliasName] = struct{}{}
	}

	sort.Strings(aliases)

	return aliases
}

// parsePHPUseStatement resolves one simple PHP use statement into its imported FQCN and local alias.
func parsePHPUseStatement(statement string) (importPath, aliasName string, ok bool) {
	trimmedStatement := strings.TrimSpace(statement)
	if trimmedStatement == "" || strings.Contains(trimmedStatement, ",") {
		return "", "", false
	}

	for _, prefix := range []string{"function ", "const "} {
		trimmedStatement = strings.TrimPrefix(trimmedStatement, prefix)
	}

	importPath = trimmedStatement
	aliasName = phpLeafFromQualifiedName(trimmedStatement)
	lowerStatement := strings.ToLower(trimmedStatement)
	if separatorIndex := strings.LastIndex(lowerStatement, " as "); separatorIndex >= 0 {
		importPath = strings.TrimSpace(trimmedStatement[:separatorIndex])
		aliasName = strings.TrimSpace(trimmedStatement[separatorIndex+len(" as "):])
	}

	importPath = strings.TrimLeft(strings.TrimSpace(importPath), `\`)
	aliasName = strings.TrimSpace(aliasName)
	if importPath == "" || aliasName == "" {
		return "", "", false
	}

	return importPath, aliasName, true
}

// phpLeafFromQualifiedName returns the last namespace component from one PHP qualified name.
func phpLeafFromQualifiedName(name string) string {
	trimmedName := strings.Trim(strings.TrimSpace(name), `\`)
	if trimmedName == "" {
		return ""
	}

	components := strings.Split(trimmedName, `\`)

	return components[len(components)-1]
}

// phpWordAlternationPattern builds one word-boundary regex that matches any of the provided candidates.
func phpWordAlternationPattern(candidates []string) (*regexp.Regexp, error) {
	uniqueCandidates := make([]string, 0, len(candidates))
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		trimmedCandidate := strings.TrimSpace(candidate)
		if trimmedCandidate == "" {
			continue
		}
		if _, exists := seen[trimmedCandidate]; exists {
			continue
		}

		uniqueCandidates = append(uniqueCandidates, regexp.QuoteMeta(trimmedCandidate))
		seen[trimmedCandidate] = struct{}{}
	}
	if len(uniqueCandidates) == 0 {
		return regexp.MustCompile(`\b\B`), nil
	}

	sort.Strings(uniqueCandidates)

	return regexp.Compile(`\b(?:` + strings.Join(uniqueCandidates, `|`) + `)\b`)
}

// phpactorRowsContain reports whether one CLI reference result set includes a specific declaration row.
func phpactorRowsContain(referenceRows []phpactorReferenceRow, file string, line int) bool {
	for _, referenceRow := range referenceRows {
		if referenceRow.File == file && referenceRow.Line == line {
			return true
		}
	}

	return false
}

// phpIsTopLevelSymbol reports whether one path has no parent symbol components.
func phpIsTopLevelSymbol(symbolPath string) bool {
	return !strings.Contains(strings.Trim(symbolPath, "/"), "/")
}

// phpParentPath extracts one symbol's parent path.
func phpParentPath(symbolPath string) (string, bool) {
	trimmedPath := strings.Trim(symbolPath, "/")
	if trimmedPath == "" {
		return "", false
	}

	components := strings.Split(trimmedPath, "/")
	if len(components) < phpReferenceTargetMinComponents {
		return "", false
	}

	return strings.Join(components[:len(components)-1], "/"), true
}

// lastPHPPathComponent returns the final slash-delimited path element.
func lastPHPPathComponent(symbolPath string) string {
	trimmedPath := strings.Trim(symbolPath, "/")
	if trimmedPath == "" {
		return ""
	}

	components := strings.Split(trimmedPath, "/")

	return components[len(components)-1]
}

// sortReferencingSymbols keeps adapter-built grouped references stable across CLI and text-backed workflows.
func sortReferencingSymbols(symbols []domain.ReferencingSymbol) {
	sort.Slice(symbols, func(leftIndex, rightIndex int) bool {
		left := symbols[leftIndex]
		right := symbols[rightIndex]
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
}
