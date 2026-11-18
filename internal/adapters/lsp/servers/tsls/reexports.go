package lsptsls

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/samber/lo"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"github.com/n-r-w/asteria/internal/domain"
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
	if request != nil {
		release, acquireErr := s.rt.AcquireSession(ctx, request.WorkspaceRoot)
		if acquireErr != nil {
			return domain.GetSymbolsOverviewResult{}, acquireErr
		}
		defer release()
	}

	result, err := s.Service.GetSymbolsOverview(ctx, request)
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}

	workspaceRoot, err := helpers.ResolveWorkspaceRoot(request.WorkspaceRoot)
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}

	cleanFile, _, err := helpers.ResolveDocumentPath(workspaceRoot, strings.TrimSpace(request.File))
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}
	resolvedAliases, err := s.resolveReexportAliases(ctx, workspaceRoot, cleanFile)
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
// when tsls omits them from documentSymbol. Warnings of the result name the files of a directory or workspace
// search that could not be processed, and count the files not searched because the search or the re-export scan
// stopped at the request deadline.
func (s *Service) FindSymbol(
	ctx context.Context,
	request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	if request != nil {
		release, acquireErr := s.rt.AcquireSession(ctx, request.WorkspaceRoot)
		if acquireErr != nil {
			return domain.FindSymbolResult{}, acquireErr
		}
		defer release()
	}

	result, failures, err := s.FindSymbolWithFailures(ctx, request)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}

	workspaceRoot, err := helpers.ResolveWorkspaceRoot(request.WorkspaceRoot)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}

	// After the deadline, the search returns what it found, without the re-export scan.
	if !failures.StopFollowUpByTimeout(ctx) {
		if scanErr := s.appendReexportScan(ctx, workspaceRoot, request, failures, &result); scanErr != nil {
			return domain.FindSymbolResult{}, scanErr
		}
	}

	sortFoundSymbolsCompat(result.Symbols)
	failures.CompleteSymbolResult(&result)

	return result, nil
}

// appendReexportScan adds the matching re-export aliases of each file of the request scope to result, records the
// files that fail in failures, and stops at the request deadline with the current file and the files after it recorded
// as not searched. It returns only an error that must end the whole search.
func (s *Service) appendReexportScan(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindSymbolRequest,
	failures *stdlsp.FileFailures,
	result *domain.FindSymbolResult,
) error {
	searchFiles, err := syntheticSearchFiles(workspaceRoot, strings.TrimSpace(request.Scope))
	if err != nil {
		return err
	}

	for fileIndex, relativePath := range searchFiles {
		// Files without matching aliases do not call the language server, so the deadline is checked before each
		// file and not only after an error.
		if failures.StopByTimeout(ctx, searchFiles[fileIndex:]) {
			return nil
		}
		appendErr := s.appendSyntheticFoundSymbols(ctx, workspaceRoot, request, relativePath, failures, result)
		if appendErr != nil {
			if failures.StopByTimeout(ctx, searchFiles[fileIndex:]) {
				return nil
			}

			return appendErr
		}
	}

	return nil
}

// appendSyntheticFoundSymbols adds the re-export aliases of one file that match the request to result, and records
// the files that fail in failures. It returns only an error that must end the whole search, or the error of the request
// context when the deadline passes before an alias, with the aliases resolved before it left in result.
func (s *Service) appendSyntheticFoundSymbols(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindSymbolRequest,
	relativePath string,
	failures *stdlsp.FileFailures,
	result *domain.FindSymbolResult,
) error {
	aliases, err := parseReexportAliases(workspaceRoot, relativePath)
	if err != nil {
		return failures.Record(ctx, relativePath, err)
	}

	trimmedPath := strings.TrimSpace(request.Path)
	for aliasIndex := range aliases {
		// Resolve only aliases that can enter the result so an unrelated unavailable package cannot abort the search.
		if !matchesSyntheticAliasPath(trimmedPath, request.SubstringMatching, aliases[aliasIndex].Name) {
			continue
		}
		// An alias whose source the language server already returned does not notice the deadline, so the deadline
		// is checked before each alias; the aliases resolved before it stay in the result.
		if domain.IsStoppedByTimeout(ctx) {
			return ctx.Err()
		}
		resolvedAlias, stopFile, resolveErr := s.resolveSyntheticAlias(
			ctx,
			workspaceRoot,
			relativePath,
			&aliases[aliasIndex],
			failures,
		)
		if stopFile {
			return resolveErr
		}
		if resolvedAlias == nil ||
			!matchesSyntheticAliasKind(request.IncludeKinds, request.ExcludeKinds, resolvedAlias.SourceKind) {
			continue
		}
		foundSymbol, buildErr := s.syntheticFoundSymbol(ctx, workspaceRoot, request, relativePath, resolvedAlias)
		if buildErr != nil {
			if recordErr := failures.Record(ctx, relativePath, buildErr); recordErr != nil {
				return recordErr
			}

			continue
		}
		appendFoundSymbolIfMissing(&result.Symbols, &foundSymbol)
	}

	return nil
}

// resolveSyntheticAlias resolves one alias of the file relativePath and records the files that fail in failures. It
// returns the resolved alias, or nil when the alias stays out because its source file failed; stopFile reports that
// the other aliases of the file must not be resolved, with the error that must end the whole search, if any.
func (s *Service) resolveSyntheticAlias(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	alias *reexportAlias,
	failures *stdlsp.FileFailures,
) (resolvedAlias *resolvedReexportAlias, stopFile bool, err error) {
	resolved, skipped, resolveErr := s.resolveReexportAliasDefinition(ctx, workspaceRoot, relativePath, alias)
	if recordErr := failures.RecordSkippedFiles(ctx, skipped); recordErr != nil {
		return nil, true, recordErr
	}
	if resolveErr != nil && helpers.IsRequestError(ctx, resolveErr) {
		return nil, true, resolveErr
	}
	if resolveErr == nil {
		return &resolved, false, nil
	}
	// A source file whose symbols cannot be read fails by itself; only the alias that needs it stays out.
	if sourceErr, ok := errors.AsType[*sourceFileError](resolveErr); ok {
		if recordErr := failures.Record(ctx, sourceErr.relativePath, sourceErr.err); recordErr != nil {
			return nil, true, recordErr
		}

		return nil, false, nil
	}

	return nil, true, failures.Record(ctx, relativePath, resolveErr)
}

// resolveReferenceTarget rewrites one re-export alias target into its defining
// source symbol when tsls omits raw references for the alias itself, and returns the neighbor files that the alias
// definition request skipped.
func (s *Service) resolveReferenceTarget(
	ctx context.Context,
	workspaceRoot string,
	request *domain.FindReferencingSymbolsRequest,
) (*domain.FindReferencingSymbolsRequest, []helpers.SkippedFile, error) {
	// Reference lookup needs at most the exact requested alias; resolving other aliases can introduce unrelated failures.
	resolvedAliases, skipped, err := s.resolveMatchingReexportAliases(
		ctx,
		workspaceRoot,
		strings.TrimSpace(request.File),
		strings.TrimSpace(request.Path),
		false,
	)
	if err != nil {
		return nil, skipped, err
	}

	for _, resolvedAlias := range resolvedAliases {
		rewrittenRequest := *request
		rewrittenRequest.File = resolvedAlias.SourceFile
		rewrittenRequest.Path = resolvedAlias.SourcePath

		return &rewrittenRequest, skipped, nil
	}

	return request, skipped, nil
}

// resolveReexportAliases resolves every explicit alias declaration needed by a complete file overview; a neighbor
// file that an alias definition request skipped only hides its declarations from that request.
func (s *Service) resolveReexportAliases(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
) ([]resolvedReexportAlias, error) {
	aliases, err := parseReexportAliases(workspaceRoot, relativePath)
	if err != nil {
		return nil, err
	}
	resolvedAliases, _, err := s.resolveParsedReexportAliases(ctx, workspaceRoot, relativePath, aliases)

	return resolvedAliases, err
}

// resolveMatchingReexportAliases filters aliases before resolution so unrelated project errors cannot affect a query,
// and returns the neighbor files that the alias definition requests skipped.
func (s *Service) resolveMatchingReexportAliases(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	query string,
	substringMatching bool,
) ([]resolvedReexportAlias, []helpers.SkippedFile, error) {
	aliases, err := parseReexportAliases(workspaceRoot, relativePath)
	if err != nil {
		return nil, nil, err
	}

	matchingAliases := lo.Filter(aliases, func(alias reexportAlias, _ int) bool {
		return matchesSyntheticAliasPath(query, substringMatching, alias.Name)
	})

	return s.resolveParsedReexportAliases(ctx, workspaceRoot, relativePath, matchingAliases)
}

// resolveParsedReexportAliases resolves the selected declarations and stops when one selected alias is invalid.
// It returns the neighbor files that the alias definition requests skipped, also when it stops.
func (s *Service) resolveParsedReexportAliases(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	aliases []reexportAlias,
) ([]resolvedReexportAlias, []helpers.SkippedFile, error) {
	result := make([]resolvedReexportAlias, 0, len(aliases))
	var skipped []helpers.SkippedFile
	for aliasIndex := range aliases {
		resolvedAlias, aliasSkipped, err := s.resolveReexportAliasDefinition(
			ctx,
			workspaceRoot,
			relativePath,
			&aliases[aliasIndex],
		)
		skipped = append(skipped, aliasSkipped...)
		if err != nil {
			return nil, skipped, err
		}
		result = append(result, resolvedAlias)
	}

	return result, skipped, nil
}

// resolveReexportAliasDefinition maps one re-export alias position to the source
// symbol that tsls resolves via definition, and returns the neighbor files that the definition request skipped.
func (s *Service) resolveReexportAliasDefinition(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	alias *reexportAlias,
) (resolvedReexportAlias, []helpers.SkippedFile, error) {
	definitionLocation, skipped, err := s.requestDefinitionLocation(
		ctx,
		workspaceRoot,
		relativePath,
		alias.SelectionRange.Start,
	)
	if err != nil && !isExpectedReexportResolutionMiss(err) {
		// Transport, process, and protocol failures are internal errors; a filesystem fallback must not hide them.
		return resolvedReexportAlias{}, skipped, err
	}
	if err == nil {
		resolvedAlias, found, resolveErr := s.reexportFromDefinitionLocation(
			ctx,
			workspaceRoot,
			relativePath,
			alias,
			definitionLocation,
		)
		if resolveErr != nil {
			return resolvedReexportAlias{}, skipped, resolveErr
		}
		if found {
			return resolvedAlias, skipped, nil
		}
	}

	// The local resolver understands source-relative imports only; package resolution belongs to TypeScript.
	if !isRelativeModuleSpecifier(alias.ModuleSpecifier) {
		return resolvedReexportAlias{}, skipped, newUnresolvedReexportError(relativePath, alias.ModuleSpecifier)
	}

	resolvedAlias, err := s.resolveExplicitReexportSource(ctx, workspaceRoot, relativePath, alias)

	return resolvedAlias, skipped, err
}

// reexportFromDefinitionLocation builds a resolved alias when TypeScript points to a usable workspace symbol.
func (s *Service) reexportFromDefinitionLocation(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	alias *reexportAlias,
	definitionLocation protocol.Location,
) (resolvedReexportAlias, bool, error) {
	sourceRelativePath, err := relativePathFromDefinitionURI(workspaceRoot, definitionLocation.URI)
	if err != nil {
		return resolvedReexportAlias{}, false, err
	}
	if sourceRelativePath == relativePath {
		return resolvedReexportAlias{}, false, nil
	}

	sourceSymbol, err := s.findDefinitionOverviewSymbol(
		ctx,
		workspaceRoot,
		sourceRelativePath,
		definitionLocation.Range.Start,
	)
	if err != nil {
		if isExpectedReexportResolutionMiss(err) {
			return resolvedReexportAlias{}, false, nil
		}
		return resolvedReexportAlias{}, false, err
	}

	return resolvedReexportAlias{
		Alias:      *alias,
		SourceFile: sourceRelativePath,
		SourcePath: sourceSymbol.Path,
		SourceKind: sourceSymbol.Kind,
	}, true, nil
}

// isExpectedReexportResolutionMiss identifies lookup misses after which the resolver tries the next source.
func isExpectedReexportResolutionMiss(err error) bool {
	toolErr, ok := errors.AsType[*domain.ToolError](err)
	return ok && toolErr.Cause() == nil
}

// isRelativeModuleSpecifier reports whether the filesystem resolver can interpret a TypeScript module specifier.
func isRelativeModuleSpecifier(moduleSpecifier string) bool {
	return moduleSpecifier == "." || moduleSpecifier == ".." ||
		strings.HasPrefix(moduleSpecifier, "./") || strings.HasPrefix(moduleSpecifier, "../")
}

// newUnresolvedReexportError reports a module specifier that the resolver cannot resolve.
func newUnresolvedReexportError(relativePath, moduleSpecifier string) *domain.ToolError {
	return domain.NewToolError(
		fmt.Sprintf(
			"cannot resolve module specifier %q from %q",
			moduleSpecifier,
			domain.NormalizeMessagePath(relativePath),
		),
		nil,
	)
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
		if hoverErr := handleSyntheticHoverError(ctx, relativePath, err); hoverErr != nil {
			return domain.FoundSymbol{}, hoverErr
		}

		return foundSymbol, nil
	}
	foundSymbol.Info = hoverInfo

	return foundSymbol, nil
}

// handleSyntheticHoverError returns a hover error of one alias only when it ends the whole request; hover info is
// optional, so any other error is logged and the alias stays without info.
func handleSyntheticHoverError(ctx context.Context, relativePath string, err error) error {
	if helpers.IsRequestError(ctx, err) {
		return err
	}

	slog.WarnContext(ctx, "skip optional synthetic hover info", "err", err, "file_path", relativePath)

	return nil
}

// requestDefinitionLocation asks tsls for one alias definition location while
// keeping the whole target directory visible to the live session, and returns the neighbor files that it skipped.
func (s *Service) requestDefinitionLocation(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	position protocol.Position,
) (protocol.Location, []helpers.SkippedFile, error) {
	workflowFiles, err := collectReferenceWorkflowFiles(workspaceRoot, relativePath)
	if err != nil {
		return protocol.Location{}, nil, err
	}

	conn, err := s.ensureConn(ctx, workspaceRoot)
	if err != nil {
		return protocol.Location{}, nil, helpers.NewSessionError(err)
	}

	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return protocol.Location{}, nil, err
	}

	var locations []protocol.Location
	// A file that cannot be opened only hides its declarations; a definition missed this way fails below.
	skipped, err := runWithReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		workflowFiles,
		s.withRequestDocument,
		func(callCtx context.Context, _ []string, _ []helpers.SkippedFile) error {
			params := &protocol.DefinitionParams{
				TextDocument: protocol.TextDocumentIdentifier{
					URI: uri.File(absolutePath),
				},
				Position:               position,
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
		return protocol.Location{}, skipped, fmt.Errorf("request definition for %q: %w", relativePath, err)
	}
	if len(locations) == 0 {
		return protocol.Location{}, skipped, domain.NewToolError(
			fmt.Sprintf("no definition matches %q", relativePath),
			nil,
		)
	}

	return locations[0], skipped, nil
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
			Depth:         syntheticOverviewDepth,
			WorkspaceRoot: workspaceRoot,
			File:          relativePath,
		},
	)
	if err != nil {
		return domain.SymbolLocation{}, newSourceFileError(relativePath, err)
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
		return domain.SymbolLocation{}, domain.NewToolError(
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
			Depth:         syntheticOverviewDepth,
			WorkspaceRoot: workspaceRoot,
			File:          relativePath,
		},
	)
	if err != nil {
		return domain.SymbolLocation{}, newSourceFileError(relativePath, err)
	}

	for _, symbol := range overview.Symbols {
		if symbol.Path == path {
			return symbol, nil
		}
	}

	return domain.SymbolLocation{}, domain.NewToolError(
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
	conn, err := s.ensureConn(ctx, workspaceRoot)
	if err != nil {
		return "", helpers.NewSessionError(err)
	}

	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return "", err
	}

	params := &protocol.HoverParams{
		TextDocument: protocol.TextDocumentIdentifier{
			URI: uri.File(absolutePath),
		},
		Position:               position,
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

	return helpers.HoverText(hover.Contents)
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

	return "", newUnresolvedReexportError(relativePath, moduleSpecifier)
}

// findDefaultExportPosition finds the `default` keyword position in one source
// file so the containing source symbol can be resolved from overview.
func findDefaultExportPosition(workspaceRoot, relativePath string) (protocol.Position, error) {
	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return protocol.Position{}, newSourceFileError(relativePath, err)
	}

	rawContent, err := os.ReadFile(filepath.Clean(absolutePath))
	if err != nil {
		return protocol.Position{}, newSourceFileError(relativePath, fmt.Errorf("read %q: %w", relativePath, err))
	}
	content := string(rawContent)
	offset := strings.Index(content, "export default")
	if offset < 0 {
		return protocol.Position{}, domain.NewToolError(
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
	absolutePath := filepath.Clean(fileURI.FsPath())
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
