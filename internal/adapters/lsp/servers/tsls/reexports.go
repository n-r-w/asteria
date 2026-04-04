package lsptsls

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

const syntheticOverviewDepth = 32

const syntheticParentDir = ".."

var reexportStatementPattern = regexp.MustCompile(`(?s)export(\s+type)?\s*\{([^}]*)\}\s*from\s*['"]([^'"]+)['"]\s*;`)

var reexportSpecifierPattern = regexp.MustCompile(`^\s*([$A-Za-z_][\w$]*)(?:\s+as\s+([$A-Za-z_][\w$]*))?\s*$`)

// reexportAlias keeps one parsed top-level TypeScript re-export alias ready for synthetic symbol handling.
type reexportAlias struct {
	OriginalName    string
	ModuleSpecifier string
	Name            string
	StatementRange  protocol.Range
	SelectionRange  protocol.Range
}

// resolvedReexportAlias keeps one parsed alias together with the source symbol that TypeScript resolves it to.
type resolvedReexportAlias struct {
	Alias      reexportAlias
	SourceFile string
	SourcePath string
	SourceKind int
}

// GetSymbolsOverview augments the shared overview with synthetic TypeScript re-export aliases when tsls omits them.
func (s *Service) GetSymbolsOverview(
	ctx context.Context,
	request *domain.GetSymbolsOverviewRequest,
) (domain.GetSymbolsOverviewResult, error) {
	result, err := s.Service.GetSymbolsOverview(ctx, request)
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}

	workspaceRoot, err := helpers.ResolveWorkspaceRoot(request.WorkspaceRoot)
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}

	resolvedAliases, err := s.resolveReexportAliases(ctx, workspaceRoot, strings.TrimSpace(request.File))
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}
	if len(resolvedAliases) == 0 {
		return result, nil
	}

	for _, resolvedAlias := range resolvedAliases {
		startLine, endLine := syntheticInclusiveLineBounds(resolvedAlias.Alias.StatementRange)
		appendOverviewSymbolIfMissing(&result.Symbols, domain.SymbolLocation{
			Kind:      resolvedAlias.SourceKind,
			Path:      resolvedAlias.Alias.Name,
			File:      strings.TrimSpace(request.File),
			StartLine: startLine,
			EndLine:   endLine,
		})
	}

	sortOverviewSymbols(result.Symbols)

	return result, nil
}

// FindSymbol augments the shared lookup with synthetic TypeScript re-export aliases
// when tsls omits them from documentSymbol.
func (s *Service) FindSymbol(
	ctx context.Context,
	request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	result, err := s.Service.FindSymbol(ctx, request)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}

	workspaceRoot, err := helpers.ResolveWorkspaceRoot(request.WorkspaceRoot)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}

	searchFiles, err := syntheticSearchFiles(
		workspaceRoot,
		strings.TrimSpace(request.Scope),
	)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}

	trimmedPath := strings.TrimSpace(request.Path)
	for _, relativePath := range searchFiles {
		resolvedAliases, resolveErr := s.resolveReexportAliases(
			ctx,
			workspaceRoot,
			relativePath,
		)
		if resolveErr != nil {
			return domain.FindSymbolResult{}, resolveErr
		}
		for _, resolvedAlias := range resolvedAliases {
			if !matchesSyntheticAliasPath(
				trimmedPath,
				request.SubstringMatching,
				resolvedAlias.Alias.Name,
			) {
				continue
			}
			if !matchesSyntheticAliasKind(
				request.IncludeKinds,
				request.ExcludeKinds,
				resolvedAlias.SourceKind,
			) {
				continue
			}
			foundSymbol, buildErr := s.syntheticFoundSymbol(
				ctx,
				workspaceRoot,
				request,
				relativePath,
				&resolvedAlias,
			)
			if buildErr != nil {
				return domain.FindSymbolResult{}, buildErr
			}
			appendFoundSymbolIfMissing(&result.Symbols, &foundSymbol)
		}
	}

	sortFoundSymbolsCompat(result.Symbols)

	return result, nil
}

// resolveReferenceTarget rewrites one re-export alias target into its defining
// source symbol when tsls omits raw references for the alias itself.
func (s *Service) resolveReferenceTarget(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindReferencingSymbolsRequest,
) (*domain.FindReferencingSymbolsRequest, error) {
	resolvedAliases, err := s.resolveReexportAliases(
		ctx,
		workspaceRoot,
		strings.TrimSpace(request.File),
	)
	if err != nil {
		return nil, err
	}

	trimmedPath := strings.TrimSpace(request.Path)
	for _, resolvedAlias := range resolvedAliases {
		if !matchesSyntheticAliasPath(trimmedPath, false, resolvedAlias.Alias.Name) {
			continue
		}

		rewrittenRequest := *request
		rewrittenRequest.File = resolvedAlias.SourceFile
		rewrittenRequest.Path = resolvedAlias.SourcePath

		return &rewrittenRequest, nil
	}

	return request, nil
}

// resolveReexportAliases parses one file and resolves each re-export alias back
// to the source symbol that TypeScript defines.
func (s *Service) resolveReexportAliases(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
) ([]resolvedReexportAlias, error) {
	aliases, err := parseReexportAliases(workspaceRoot, relativePath)
	if err != nil {
		return nil, err
	}
	if len(aliases) == 0 {
		return nil, nil
	}

	result := make([]resolvedReexportAlias, 0, len(aliases))
	for aliasIndex := range aliases {
		resolvedAlias, resolveErr := s.resolveReexportAliasDefinition(
			ctx,
			workspaceRoot,
			relativePath,
			&aliases[aliasIndex],
		)
		if resolveErr != nil {
			return nil, resolveErr
		}
		result = append(result, resolvedAlias)
	}

	return result, nil
}

// resolveReexportAliasDefinition maps one re-export alias position to the source
// symbol that tsls resolves via definition.
func (s *Service) resolveReexportAliasDefinition(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	alias *reexportAlias,
) (resolvedReexportAlias, error) {
	definitionLocation, err := s.requestDefinitionLocation(
		ctx,
		workspaceRoot,
		relativePath,
		alias.SelectionRange.Start,
	)
	if err == nil {
		sourceRelativePath, relativeErr := relativePathFromDefinitionURI(workspaceRoot, definitionLocation.URI)
		if relativeErr == nil && sourceRelativePath != relativePath {
			sourceSymbol, sourceErr := s.findDefinitionOverviewSymbol(
				ctx,
				workspaceRoot,
				sourceRelativePath,
				definitionLocation.Range.Start,
			)
			if sourceErr == nil {
				return resolvedReexportAlias{
					Alias:      *alias,
					SourceFile: sourceRelativePath,
					SourcePath: sourceSymbol.Path,
					SourceKind: sourceSymbol.Kind,
				}, nil
			}
		}
	}

	return s.resolveExplicitReexportSource(ctx, workspaceRoot, relativePath, alias)
}

// resolveExplicitReexportSource resolves one alias through its explicit `from`
// clause when definition does not jump to the source file.
func (s *Service) resolveExplicitReexportSource(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	alias *reexportAlias,
) (resolvedReexportAlias, error) {
	sourceRelativePath, err := resolveReexportSourceFile(
		workspaceRoot,
		relativePath,
		alias.ModuleSpecifier,
	)
	if err != nil {
		return resolvedReexportAlias{}, err
	}

	var sourceSymbol domain.SymbolLocation
	if alias.OriginalName == "default" {
		defaultPosition, positionErr := findDefaultExportPosition(
			workspaceRoot,
			sourceRelativePath,
		)
		if positionErr != nil {
			return resolvedReexportAlias{}, positionErr
		}
		sourceSymbol, err = s.findDefinitionOverviewSymbol(
			ctx,
			workspaceRoot,
			sourceRelativePath,
			defaultPosition,
		)
	} else {
		sourceSymbol, err = s.findOverviewSymbolByPath(
			ctx,
			workspaceRoot,
			sourceRelativePath,
			alias.OriginalName,
		)
	}
	if err != nil {
		return resolvedReexportAlias{}, err
	}

	return resolvedReexportAlias{
		Alias:      *alias,
		SourceFile: sourceRelativePath,
		SourcePath: sourceSymbol.Path,
		SourceKind: sourceSymbol.Kind,
	}, nil
}

// syntheticFoundSymbol materializes one synthetic alias result while keeping the
// alias file and alias range visible to callers.
func (s *Service) syntheticFoundSymbol(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindSymbolRequest,
	relativePath string,
	resolvedAlias *resolvedReexportAlias,
) (domain.FoundSymbol, error) {
	startLine, endLine := syntheticInclusiveLineBounds(resolvedAlias.Alias.StatementRange)
	foundSymbol := domain.FoundSymbol{
		Kind:      resolvedAlias.SourceKind,
		Body:      "",
		Info:      "",
		Path:      resolvedAlias.Alias.Name,
		File:      relativePath,
		StartLine: startLine,
		EndLine:   endLine,
	}

	fileCache := map[string]string{}
	if request.IncludeBody {
		body, err := readSyntheticSymbolBody(
			workspaceRoot,
			relativePath,
			resolvedAlias.Alias.StatementRange,
			fileCache,
		)
		if err != nil {
			return domain.FoundSymbol{}, err
		}
		foundSymbol.Body = body
	}
	if !request.IncludeInfo {
		return foundSymbol, nil
	}

	hoverInfo, err := s.requestSyntheticHoverInfo(
		ctx,
		workspaceRoot,
		relativePath,
		resolvedAlias.Alias.SelectionRange.Start,
	)
	if err != nil {
		return domain.FoundSymbol{}, err
	}
	foundSymbol.Info = hoverInfo

	return foundSymbol, nil
}

// requestDefinitionLocation asks tsls for one alias definition location while
// keeping the whole target directory visible to the live session.
func (s *Service) requestDefinitionLocation(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	position protocol.Position,
) (protocol.Location, error) {
	workflowFiles, err := collectReferenceWorkflowFiles(workspaceRoot, relativePath)
	if err != nil {
		return protocol.Location{}, err
	}

	conn, err := s.rt.EnsureConn(ctx, workspaceRoot)
	if err != nil {
		return protocol.Location{}, err
	}

	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return protocol.Location{}, err
	}

	var locations []protocol.Location
	err = runWithReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		workflowFiles,
		s.withRequestDocument,
		func(callCtx context.Context) error {
			params := &protocol.DefinitionParams{
				TextDocumentPositionParams: protocol.TextDocumentPositionParams{
					TextDocument: protocol.TextDocumentIdentifier{
						URI: uri.File(absolutePath),
					},
					Position: position,
				},
				WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
				PartialResultParams:    protocol.PartialResultParams{},
			}

			return protocol.Call(
				callCtx,
				conn,
				protocol.MethodTextDocumentDefinition,
				params,
				&locations,
			)
		},
	)
	if err != nil {
		return protocol.Location{}, fmt.Errorf("request definition for %q: %w", relativePath, err)
	}
	if len(locations) == 0 {
		return protocol.Location{}, domain.NewSafeError(
			fmt.Sprintf("no definition matches %q", relativePath),
			nil,
		)
	}

	return locations[0], nil
}

// findDefinitionOverviewSymbol resolves one source definition position back to
// the smallest overview symbol that contains it.
func (s *Service) findDefinitionOverviewSymbol(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	position protocol.Position,
) (domain.SymbolLocation, error) {
	overview, err := s.Service.GetSymbolsOverview(
		ctx,
		&domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: syntheticOverviewDepth},
			WorkspaceRoot:            workspaceRoot,
			File:                     relativePath,
		},
	)
	if err != nil {
		return domain.SymbolLocation{}, err
	}

	var match domain.SymbolLocation
	found := false
	for _, symbol := range overview.Symbols {
		if !positionInLocation(position, symbol) {
			continue
		}
		if !found || syntheticSymbolSpan(symbol) < syntheticSymbolSpan(match) {
			match = symbol
			found = true
		}
	}
	if !found {
		return domain.SymbolLocation{}, domain.NewSafeError(
			fmt.Sprintf(
				"no source symbol contains %q:%d:%d",
				relativePath,
				position.Line,
				position.Character,
			),
			nil,
		)
	}

	return match, nil
}

// findOverviewSymbolByPath resolves one exact source symbol path from the
// shared overview surface.
func (s *Service) findOverviewSymbolByPath(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	path string,
) (domain.SymbolLocation, error) {
	overview, err := s.Service.GetSymbolsOverview(
		ctx,
		&domain.GetSymbolsOverviewRequest{
			GetSymbolsOverviewFilter: domain.GetSymbolsOverviewFilter{Depth: syntheticOverviewDepth},
			WorkspaceRoot:            workspaceRoot,
			File:                     relativePath,
		},
	)
	if err != nil {
		return domain.SymbolLocation{}, err
	}

	for _, symbol := range overview.Symbols {
		if symbol.Path == path {
			return symbol, nil
		}
	}

	return domain.SymbolLocation{}, domain.NewSafeError(
		fmt.Sprintf("no source symbol matches %q", path),
		nil,
	)
}

// requestSyntheticHoverInfo asks tsls for hover text at one synthetic alias position.
func (s *Service) requestSyntheticHoverInfo(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	position protocol.Position,
) (string, error) {
	conn, err := s.rt.EnsureConn(ctx, workspaceRoot)
	if err != nil {
		return "", err
	}

	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return "", err
	}

	params := &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{
				URI: uri.File(absolutePath),
			},
			Position: position,
		},
		WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
	}

	var hover protocol.Hover
	err = s.withRequestDocument(ctx, conn, absolutePath, func(callCtx context.Context) error {
		return protocol.Call(
			callCtx,
			conn,
			protocol.MethodTextDocumentHover,
			params,
			&hover,
		)
	})
	if err != nil {
		return "", fmt.Errorf("request hover for %q: %w", relativePath, err)
	}

	return strings.TrimSpace(hover.Contents.Value), nil
}

// parseReexportAliases extracts top-level `export { ... } from` and
// `export type { ... } from` aliases from one TS file.
func parseReexportAliases(workspaceRoot, relativePath string) ([]reexportAlias, error) {
	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return nil, err
	}

	rawContent, err := os.ReadFile(filepath.Clean(absolutePath))
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", relativePath, err)
	}
	content := string(rawContent)

	matches := reexportStatementPattern.FindAllStringSubmatchIndex(content, -1)
	if len(matches) == 0 {
		return nil, nil
	}

	aliases := make([]reexportAlias, 0)
	for _, match := range matches {
		statementStart, statementEnd := match[0], match[1]
		moduleSpecifierStart, moduleSpecifierEnd := match[6], match[7]
		specsStart, specsEnd := match[4], match[5]
		specsText := content[specsStart:specsEnd]
		moduleSpecifier := content[moduleSpecifierStart:moduleSpecifierEnd]
		segmentStart := 0
		for rawSegment := range strings.SplitSeq(specsText, ",") {
			alias, ok := parseReexportSpecifier(
				content,
				statementStart,
				statementEnd,
				specsStart,
				segmentStart,
				rawSegment,
				moduleSpecifier,
			)
			if ok {
				aliases = append(aliases, alias)
			}
			segmentStart += len(rawSegment) + 1
		}
	}

	return aliases, nil
}

// parseReexportSpecifier resolves one comma-separated export specifier into the
// alias name and source range in the file.
func parseReexportSpecifier(
	content string,
	statementStart int,
	statementEnd int,
	specsStart int,
	segmentStart int,
	rawSegment string,
	moduleSpecifier string,
) (reexportAlias, bool) {
	indexes := reexportSpecifierPattern.FindStringSubmatchIndex(rawSegment)
	if indexes == nil {
		return reexportAlias{}, false
	}

	aliasName := ""
	if indexes[4] >= 0 && indexes[5] >= 0 {
		aliasName = rawSegment[indexes[4]:indexes[5]]
	}
	if aliasName == "" {
		aliasName = rawSegment[indexes[2]:indexes[3]]
	}

	aliasRelativeStart := indexes[4]
	aliasRelativeEnd := indexes[5]
	if aliasRelativeStart < 0 || aliasRelativeEnd < 0 {
		aliasRelativeStart = indexes[2]
		aliasRelativeEnd = indexes[3]
	}
	aliasAbsoluteStart := specsStart + segmentStart + aliasRelativeStart
	aliasAbsoluteEnd := specsStart + segmentStart + aliasRelativeEnd

	return reexportAlias{
		OriginalName:    rawSegment[indexes[2]:indexes[3]],
		ModuleSpecifier: moduleSpecifier,
		Name:            aliasName,
		StatementRange: protocol.Range{
			Start: offsetToPosition(content, statementStart),
			End:   offsetToPosition(content, statementEnd),
		},
		SelectionRange: protocol.Range{
			Start: offsetToPosition(content, aliasAbsoluteStart),
			End:   offsetToPosition(content, aliasAbsoluteEnd),
		},
	}, true
}

// resolveReexportSourceFile resolves one `from` module specifier relative to
// the alias file and normalizes it to a workspace-relative path.
func resolveReexportSourceFile(workspaceRoot, relativePath, moduleSpecifier string) (string, error) {
	aliasDir := filepath.Dir(filepath.Join(workspaceRoot, filepath.FromSlash(relativePath)))
	basePath := filepath.Join(aliasDir, filepath.FromSlash(moduleSpecifier))
	if filepath.Ext(basePath) != "" {
		relativeSourcePath, err := filepath.Rel(workspaceRoot, basePath)
		if err != nil {
			return "", err
		}
		return filepath.ToSlash(relativeSourcePath), nil
	}

	for _, extension := range extensions {
		candidatePath := basePath + extension
		if _, err := os.Stat(filepath.Clean(candidatePath)); err == nil {
			relativeSourcePath, relativeErr := filepath.Rel(workspaceRoot, candidatePath)
			if relativeErr != nil {
				return "", relativeErr
			}
			return filepath.ToSlash(relativeSourcePath), nil
		}
	}

	return "", fmt.Errorf("no source file matches module specifier %q", moduleSpecifier)
}

// findDefaultExportPosition finds the `default` keyword position in one source
// file so the containing source symbol can be resolved from overview.
func findDefaultExportPosition(workspaceRoot, relativePath string) (protocol.Position, error) {
	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return protocol.Position{}, err
	}

	rawContent, err := os.ReadFile(filepath.Clean(absolutePath))
	if err != nil {
		return protocol.Position{}, fmt.Errorf("read %q: %w", relativePath, err)
	}
	content := string(rawContent)
	offset := strings.Index(content, "export default")
	if offset < 0 {
		return protocol.Position{}, domain.NewSafeError(
			fmt.Sprintf("no default export found in %q", relativePath),
			nil,
		)
	}

	return offsetToPosition(content, offset+len("export ")), nil
}

// offsetToPosition converts one byte offset in the ASCII fixture content into
// an LSP position.
func offsetToPosition(content string, offset int) protocol.Position {
	if offset < 0 {
		offset = 0
	}
	if offset > len(content) {
		offset = len(content)
	}

	prefix := content[:offset]
	line := safeUint32FromInt(strings.Count(prefix, "\n"))
	lastNewlineIndex := strings.LastIndex(prefix, "\n")
	character := safeUint32FromInt(offset)
	if lastNewlineIndex >= 0 {
		character = safeUint32FromInt(offset - lastNewlineIndex - 1)
	}

	return protocol.Position{Line: line, Character: character}
}

// positionInLocation checks whether one source position falls inside one
// overview symbol location.
func positionInLocation(position protocol.Position, symbol domain.SymbolLocation) bool {
	start := protocol.Position{Line: safeUint32FromInt(symbol.StartLine), Character: 0}
	end := protocol.Position{Line: safeUint32FromInt(symbol.EndLine + 1), Character: 0}

	return compareSyntheticPositions(position, start) >= 0 &&
		compareSyntheticPositions(position, end) < 0
}

// compareSyntheticPositions orders two LSP positions without allocations.
func compareSyntheticPositions(left, right protocol.Position) int {
	if left.Line != right.Line {
		if left.Line < right.Line {
			return -1
		}

		return 1
	}
	if left.Character < right.Character {
		return -1
	}
	if left.Character > right.Character {
		return 1
	}

	return 0
}

// syntheticSymbolSpan keeps the smallest containing symbol selection
// deterministic across overview rows.
func syntheticSymbolSpan(symbol domain.SymbolLocation) int {
	return symbol.EndLine - symbol.StartLine
}

// syntheticSearchFiles resolves the files that need synthetic alias scanning
// for one find_symbol request scope.
func syntheticSearchFiles(workspaceRoot, scope string) ([]string, error) {
	trimmedScope := strings.TrimSpace(scope)
	if trimmedScope == "" {
		return walkSyntheticSearchFiles(workspaceRoot, ".")
	}

	cleanScope := filepath.Clean(trimmedScope)
	absoluteScope := filepath.Join(workspaceRoot, filepath.FromSlash(cleanScope))
	relativeScope, err := filepath.Rel(workspaceRoot, absoluteScope)
	if err != nil {
		return nil, err
	}
	if relativeScope == syntheticParentDir ||
		strings.HasPrefix(relativeScope, syntheticParentDir+string(filepath.Separator)) {
		return nil, fmt.Errorf("relative path %q escapes workspace root", trimmedScope)
	}

	info, err := os.Stat(filepath.Clean(absoluteScope))
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if !supportsSyntheticSearchFile(relativeScope) {
			return nil, nil
		}
		return []string{filepath.ToSlash(relativeScope)}, nil
	}

	return walkSyntheticSearchFiles(workspaceRoot, relativeScope)
}

// walkSyntheticSearchFiles traverses one relative directory while keeping
// TypeScript ignore rules consistent with the adapter.
func walkSyntheticSearchFiles(workspaceRoot, relativeDir string) ([]string, error) {
	absoluteDir := workspaceRoot
	if relativeDir != "." {
		absoluteDir = filepath.Join(workspaceRoot, filepath.FromSlash(relativeDir))
	}

	searchFiles := make([]string, 0)
	err := filepath.WalkDir(
		filepath.Clean(absoluteDir),
		func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}

			relativePath, err := filepath.Rel(workspaceRoot, path)
			if err != nil {
				return err
			}
			relativePath = filepath.ToSlash(relativePath)
			if entry.IsDir() {
				if relativePath != "." && shouldIgnoreDir(relativePath) {
					return filepath.SkipDir
				}
				return nil
			}
			if !supportsSyntheticSearchFile(relativePath) {
				return nil
			}

			searchFiles = append(searchFiles, relativePath)

			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	sort.Strings(searchFiles)

	return searchFiles, nil
}

// supportsSyntheticSearchFile keeps synthetic alias fallback aligned with the
// adapter's supported source files.
func supportsSyntheticSearchFile(relativePath string) bool {
	extension := filepath.Ext(relativePath)
	return slices.Contains(extensions, extension)
}

// matchesSyntheticAliasPath applies the subset of stdlsp path semantics that
// synthetic top-level aliases support.
func matchesSyntheticAliasPath(query string, substringMatching bool, aliasName string) bool {
	trimmedQuery := strings.TrimSpace(query)
	trimmedQuery = strings.TrimPrefix(trimmedQuery, "/")
	if trimmedQuery == "" || strings.Contains(trimmedQuery, "/") {
		return false
	}
	if substringMatching {
		return strings.Contains(strings.ToLower(aliasName), strings.ToLower(trimmedQuery))
	}

	return aliasName == trimmedQuery
}

// matchesSyntheticAliasKind keeps synthetic alias filtering consistent with
// shared kind include/exclude behavior.
func matchesSyntheticAliasKind(includeKinds, excludeKinds []int, kind int) bool {
	if slices.Contains(excludeKinds, kind) {
		return false
	}
	if len(includeKinds) == 0 {
		return true
	}

	return slices.Contains(includeKinds, kind)
}

// appendOverviewSymbolIfMissing keeps overview augmentation stable if tsls ever
// starts returning native alias symbols.
func appendOverviewSymbolIfMissing(symbols *[]domain.SymbolLocation, symbol domain.SymbolLocation) {
	for _, existingSymbol := range *symbols {
		if existingSymbol.Path == symbol.Path &&
			existingSymbol.File == symbol.File &&
			existingSymbol.StartLine == symbol.StartLine {
			return
		}
	}

	*symbols = append(*symbols, symbol)
}

// appendFoundSymbolIfMissing keeps synthetic find_symbol augmentation
// idempotent across native and synthetic matches.
func appendFoundSymbolIfMissing(symbols *[]domain.FoundSymbol, symbol *domain.FoundSymbol) {
	for _, existingSymbol := range *symbols {
		if existingSymbol.Path == symbol.Path &&
			existingSymbol.File == symbol.File &&
			existingSymbol.StartLine == symbol.StartLine {
			return
		}
	}

	*symbols = append(*symbols, *symbol)
}

// sortOverviewSymbols keeps augmented overview rows stable across native and
// synthetic orderings.
func sortOverviewSymbols(symbols []domain.SymbolLocation) {
	sort.SliceStable(symbols, func(leftIndex, rightIndex int) bool {
		left := symbols[leftIndex]
		right := symbols[rightIndex]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.StartLine != right.StartLine {
			return left.StartLine < right.StartLine
		}
		if left.EndLine != right.EndLine {
			return left.EndLine < right.EndLine
		}
		if left.Path != right.Path {
			return left.Path < right.Path
		}

		return left.Kind < right.Kind
	})
}

// sortFoundSymbolsCompat keeps augmented find_symbol output deterministic
// without reaching into stdlsp internals.
func sortFoundSymbolsCompat(symbols []domain.FoundSymbol) {
	sort.Slice(symbols, func(leftIndex, rightIndex int) bool {
		left := symbols[leftIndex]
		right := symbols[rightIndex]
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
}

// readSyntheticSymbolBody slices one synthetic alias statement from disk using
// the alias file range.
func readSyntheticSymbolBody(
	workspaceRoot string,
	relativePath string,
	targetRange protocol.Range,
	fileCache map[string]string,
) (string, error) {
	content, err := stdlspReadFileContent(workspaceRoot, relativePath, fileCache)
	if err != nil {
		return "", err
	}

	return syntheticSliceContentByRange(content, targetRange), nil
}

// relativePathFromDefinitionURI converts one definition URI into a
// workspace-relative path rooted at the active request workspace.
func relativePathFromDefinitionURI(workspaceRoot string, fileURI uri.URI) (string, error) {
	absolutePath := filepath.Clean(fileURI.Filename())
	normalizedAbsolutePath, err := filepath.EvalSymlinks(absolutePath)
	if err == nil {
		absolutePath = normalizedAbsolutePath
	}

	relativePath, err := filepath.Rel(workspaceRoot, absolutePath)
	if err != nil {
		return "", fmt.Errorf("resolve %q against workspace: %w", absolutePath, err)
	}
	if relativePath == syntheticParentDir ||
		strings.HasPrefix(relativePath, syntheticParentDir+string(filepath.Separator)) {
		return "", fmt.Errorf("definition path %q points outside the workspace", absolutePath)
	}

	return filepath.ToSlash(relativePath), nil
}

// syntheticInclusiveLineBounds converts one LSP range into 0-based inclusive
// line bounds.
func syntheticInclusiveLineBounds(targetRange protocol.Range) (startLine, endLine int) {
	startLine = int(targetRange.Start.Line)
	endLine = int(targetRange.End.Line)
	if targetRange.End.Character == 0 && endLine > startLine {
		endLine--
	}
	if endLine < startLine {
		endLine = startLine
	}

	return startLine, endLine
}

// stdlspReadFileContent mirrors stdlsp file caching so synthetic alias bodies
// do not re-read the same file repeatedly.
func stdlspReadFileContent(workspaceRootPath, relativePath string, fileCache map[string]string) (string, error) {
	if cachedContent, ok := fileCache[relativePath]; ok {
		return cachedContent, nil
	}

	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRootPath, relativePath)
	if err != nil {
		return "", err
	}

	rawContent, err := os.ReadFile(filepath.Clean(absolutePath))
	if err != nil {
		return "", fmt.Errorf("read %q: %w", relativePath, err)
	}

	content := string(rawContent)
	fileCache[relativePath] = content

	return content, nil
}

// syntheticSliceContentByRange extracts text for one LSP range using the alias
// file's source content.
func syntheticSliceContentByRange(content string, targetRange protocol.Range) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return ""
	}

	startLine := min(max(0, int(targetRange.Start.Line)), len(lines)-1)
	endLine := min(max(0, int(targetRange.End.Line)), len(lines)-1)
	if startLine > endLine {
		return ""
	}
	if startLine == endLine {
		line := lines[startLine]
		startColumn := min(max(0, int(targetRange.Start.Character)), len(line))
		endColumn := min(max(startColumn, int(targetRange.End.Character)), len(line))
		return line[startColumn:endColumn]
	}

	parts := make([]string, 0, endLine-startLine+1)
	startColumn := min(
		max(0, int(targetRange.Start.Character)),
		len(lines[startLine]),
	)
	parts = append(parts, lines[startLine][startColumn:])
	for lineIndex := startLine + 1; lineIndex < endLine; lineIndex++ {
		parts = append(parts, lines[lineIndex])
	}
	endColumn := min(max(0, int(targetRange.End.Character)), len(lines[endLine]))
	parts = append(parts, lines[endLine][:endColumn])

	return strings.Join(parts, "\n")
}

// safeUint32FromInt bounds-checks int to uint32 conversions so file offsets
// stay explicit and lint-clean.
func safeUint32FromInt(value int) uint32 {
	if value <= 0 {
		return 0
	}
	if value > int(^uint32(0)) {
		return ^uint32(0)
	}

	return uint32(value)
}
