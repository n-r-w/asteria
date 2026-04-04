package stdlsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"github.com/n-r-w/asteria/internal/server"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
	"go.lsp.dev/uri"
)

// Service coordinates symbol overview, symbol search, and reference search over one standard LSP connection.
type Service struct {
	config               *Config
	cacheDisableWarnings sync.Map
}

var _ server.ILSP = (*Service)(nil)

// New validates stdlsp dependencies once so adapter calls can stay focused on behavior.
func New(config *Config) (*Service, error) {
	if config == nil {
		return nil, errors.New("stdlsp config is nil")
	}

	var err error
	if len(config.Extensions) == 0 {
		err = errors.Join(err, errors.New("at least one extension is required"))
	}
	if config.EnsureConn == nil {
		err = errors.Join(err, errors.New("ensure conn callback is required"))
	}
	if config.OpenFileForDocumentSymbol && config.WithRequestDocument == nil {
		err = errors.Join(
			err,
			errors.New("with request document callback is required when open_file_for_document_symbol is enabled"),
		)
	}
	if config.OpenFileForReferenceWorkflow && config.WithRequestDocument == nil {
		err = errors.Join(
			err,
			errors.New("with request document callback is required when open_file_for_reference_workflow is enabled"),
		)
	}
	if (config.SymbolTreeCache == nil) != (config.BuildSymbolTreeCacheMetadata == nil) {
		err = errors.Join(
			err,
			errors.New("symbol tree cache and cache metadata builder must be configured together"),
		)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid stdlsp config: %w", err)
	}

	return &Service{config: &Config{
		Extensions:                   config.Extensions,
		EnsureConn:                   config.EnsureConn,
		WithRequestDocument:          config.WithRequestDocument,
		OpenFileForDocumentSymbol:    config.OpenFileForDocumentSymbol,
		OpenFileForReferenceWorkflow: config.OpenFileForReferenceWorkflow,
		BuildNamePath:                config.BuildNamePath,
		IgnoreDir:                    config.IgnoreDir,
		SymbolTreeCache:              config.SymbolTreeCache,
		BuildSymbolTreeCacheMetadata: config.BuildSymbolTreeCacheMetadata,
	}, cacheDisableWarnings: sync.Map{}}, nil
}

// GetSymbolsOverview asks the language server for one file's symbols and maps them to the domain overview contract.
func (s *Service) GetSymbolsOverview(
	ctx context.Context,
	request *domain.GetSymbolsOverviewRequest,
) (domain.GetSymbolsOverviewResult, error) {
	if err := request.Validate(); err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}

	workspaceRoot, err := helpers.ResolveWorkspaceRoot(request.WorkspaceRoot)
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}

	_, symbolTree, err := s.loadSymbolTree(ctx, workspaceRoot, request.File)
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}

	return nodeTreeToOverview(request.Depth, symbolTree)
}

// FindSymbol resolves normalized name-path queries by walking standard-LSP symbol trees.
func (s *Service) FindSymbol(
	ctx context.Context,
	request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	if err := request.Validate(); err != nil {
		return domain.FindSymbolResult{}, err
	}
	workspaceRoot, err := helpers.ResolveWorkspaceRoot(request.WorkspaceRoot)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}

	trimmedPath := strings.TrimSpace(request.Path)
	trimmedScope := strings.TrimSpace(request.Scope)

	scope, err := resolveSearchScope(workspaceRoot, trimmedScope)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}

	searchFiles, err := collectScopeFiles(workspaceRoot, scope, s.config.Extensions, s.config.IgnoreDir)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}

	matcher := newNamePathMatcher(trimmedPath, request.SubstringMatching)
	fileCache := make(map[string]string)
	hoverInfoByKey := make(map[string]string)
	result := domain.FindSymbolResult{Symbols: make([]domain.FoundSymbol, 0)}

	for _, relativePath := range searchFiles {
		_, symbolTree, treeErr := s.loadSymbolTree(ctx, workspaceRoot, relativePath)
		if treeErr != nil {
			return domain.FindSymbolResult{}, treeErr
		}

		matchedNodes := collectMatchedNodesForRequest(
			symbolTree,
			matcher,
			request.Depth,
			request.IncludeKinds,
			request.ExcludeKinds,
		)
		for _, matchedNode := range matchedNodes {
			foundSymbol := s.buildFoundSymbol(ctx, workspaceRoot, matchedNode, request, fileCache, hoverInfoByKey)
			result.Symbols = append(result.Symbols, foundSymbol)
		}
	}

	sortFoundSymbols(result.Symbols)

	return result, nil
}

// FindReferencingSymbols resolves the target symbol, requests references,
// and shapes the grouped result through one shared workflow.
func (s *Service) FindReferencingSymbols(
	ctx context.Context,
	request *domain.FindReferencingSymbolsRequest,
) (domain.FindReferencingSymbolsResult, error) {
	if err := request.Validate(); err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}
	workspaceRoot, err := helpers.ResolveWorkspaceRoot(request.WorkspaceRoot)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	trimmedFile := strings.TrimSpace(request.File)
	trimmedPath := strings.TrimSpace(request.Path)
	var result domain.FindReferencingSymbolsResult

	runReferenceWorkflow := func(callCtx context.Context) error {
		var callErr error
		result, callErr = s.findReferencingSymbolsResult(callCtx, workspaceRoot, trimmedFile, trimmedPath, request)

		return callErr
	}

	if !s.config.OpenFileForReferenceWorkflow {
		err = runReferenceWorkflow(ctx)
		if err != nil {
			return domain.FindReferencingSymbolsResult{}, err
		}

		return result, nil
	}

	_, targetAbsolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, trimmedFile)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	conn, err := s.config.EnsureConn(ctx, workspaceRoot)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	err = s.runRequestWithDocumentContext(ctx, conn, targetAbsolutePath, runReferenceWorkflow)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	return result, nil
}

// findReferencingSymbolsResult runs the shared references workflow and returns the grouped result.
func (s *Service) findReferencingSymbolsResult(
	ctx context.Context,
	workspaceRoot string,
	trimmedFile string,
	trimmedPath string,
	request *domain.FindReferencingSymbolsRequest,
) (domain.FindReferencingSymbolsResult, error) {
	targetRelativePath, targetTree, targetSymbol, err := s.resolveTargetSymbol(
		ctx,
		workspaceRoot,
		trimmedFile,
		trimmedPath,
	)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	fileCache := make(map[string]string)
	referencePosition := targetSymbol.SelectionRange.Start

	referenceLocations, err := s.requestReferenceLocations(
		ctx,
		workspaceRoot,
		targetSymbol.RelativePath,
		referencePosition,
	)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	referenceMatches, err := s.collectReferenceMatches(
		ctx,
		workspaceRoot,
		referenceLocations,
		targetRelativePath,
		targetTree,
		request,
		fileCache,
	)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	return domain.FindReferencingSymbolsResult{Symbols: groupReferenceMatches(referenceMatches)}, nil
}

// loadSymbolTree loads one file's symbols and maps them into the normalized stdlsp tree.
func (s *Service) loadSymbolTree(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
) (string, []*node, error) {
	cleanRelativePath, _, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return "", nil, err
	}

	cacheMetadata, cacheEnabled, err := s.cacheMetadata(ctx, workspaceRoot, cleanRelativePath)
	if err != nil {
		return "", nil, err
	}
	if cacheEnabled {
		cachedTree, found, readErr := s.readSymbolTreeFromCache(ctx, workspaceRoot, cleanRelativePath, cacheMetadata)
		if readErr != nil {
			slog.WarnContext(
				ctx,
				"read symbol tree cache entry",
				"adapter_id", cacheMetadata.AdapterID,
				"profile_id", cacheMetadata.ProfileID,
				"workspace_root", workspaceRoot,
				"relative_path", cleanRelativePath,
				"err", readErr,
			)
		} else if found {
			return cleanRelativePath, cachedTree, nil
		}
	}

	_, rawSymbols, err := s.requestRawDocumentSymbols(ctx, workspaceRoot, cleanRelativePath)
	if err != nil {
		return "", nil, err
	}

	symbolTree, err := mapRawSymbolsToTree(cleanRelativePath, rawSymbols, s.config.BuildNamePath)
	if err != nil {
		return "", nil, err
	}
	if cacheEnabled {
		s.writeSymbolTreeToCache(ctx, workspaceRoot, cleanRelativePath, cacheMetadata, symbolTree)
	}

	return cleanRelativePath, symbolTree, nil
}

// resolveTargetSymbol loads one file's symbol tree and resolves exactly one requested target symbol from it.
func (s *Service) resolveTargetSymbol(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	namePath string,
) (
	cleanRelativePath string,
	symbolTree []*node,
	targetSymbol *node,
	err error,
) {
	cleanRelativePath, symbolTree, err = s.loadSymbolTree(ctx, workspaceRoot, relativePath)
	if err != nil {
		return "", nil, nil, err
	}

	targetSymbol, err = findUniqueNode(symbolTree, strings.TrimSpace(namePath))
	if err != nil {
		return "", nil, nil, err
	}

	return cleanRelativePath, symbolTree, targetSymbol, nil
}

// collectReferenceMatches resolves raw reference locations into flat rows before final grouping.
func (s *Service) collectReferenceMatches(
	ctx context.Context,
	workspaceRoot string,
	referenceLocations []protocol.Location,
	targetRelativePath string,
	targetTree []*node,
	request *domain.FindReferencingSymbolsRequest,
	fileCache map[string]string,
) ([]referenceMatch, error) {
	referenceMatches := make([]referenceMatch, 0, len(referenceLocations))
	containerTreeCache := map[string][]*node{targetRelativePath: targetTree}

	for _, location := range referenceLocations {
		resolvedMatch, ok, err := s.referenceMatchFromLocation(
			ctx,
			workspaceRoot,
			location,
			request,
			fileCache,
			containerTreeCache,
		)
		if err != nil {
			return nil, err
		}
		if ok {
			referenceMatches = append(referenceMatches, resolvedMatch)
		}
	}

	return referenceMatches, nil
}

// referenceMatchFromLocation resolves one raw LSP location into one grouped reference row.
func (s *Service) referenceMatchFromLocation(
	ctx context.Context,
	workspaceRoot string,
	location protocol.Location,
	request *domain.FindReferencingSymbolsRequest,
	fileCache map[string]string,
	containerTreeCache map[string][]*node,
) (referenceMatch, bool, error) {
	referenceRelativePath, err := s.relativePathFromURI(workspaceRoot, location.URI)
	if err != nil {
		return referenceMatch{}, false, err
	}

	containerTree, err := s.symbolTreeForReferencePath(ctx, workspaceRoot, referenceRelativePath, containerTreeCache)
	if err != nil {
		return referenceMatch{}, false, err
	}

	evidence, err := referenceEvidenceCandidateFromRange(
		workspaceRoot,
		referenceRelativePath,
		location.Range,
		fileCache,
	)
	if err != nil {
		return referenceMatch{}, false, err
	}

	container, ok := findContainingNode(containerTree, location.Range.Start)
	if ok {
		if !matchesKindFilters(container.Kind, request.IncludeKinds, request.ExcludeKinds) {
			return referenceMatch{}, false, nil
		}

		containerStartLine, containerEndLine := inclusiveLineBounds(container.Range)

		return referenceMatch{
			Container: domain.SymbolLocation{
				Kind:      container.Kind,
				Path:      container.NamePath,
				File:      container.RelativePath,
				StartLine: containerStartLine,
				EndLine:   containerEndLine,
			},
			Evidence: evidence,
		}, true, nil
	}

	fallbackContainer, fallbackOK, err := fileFallbackReferenceContainer(
		workspaceRoot,
		referenceRelativePath,
		request.IncludeKinds,
		request.ExcludeKinds,
		fileCache,
	)
	if err != nil {
		return referenceMatch{}, false, err
	}
	if !fallbackOK {
		return referenceMatch{}, false, nil
	}

	return referenceMatch{Container: fallbackContainer, Evidence: evidence}, true, nil
}

// symbolTreeForReferencePath loads and caches the normalized symbol tree for one referenced file.
func (s *Service) symbolTreeForReferencePath(
	ctx context.Context,
	workspaceRoot string,
	referenceRelativePath string,
	containerTreeCache map[string][]*node,
) ([]*node, error) {
	if containerTree, ok := containerTreeCache[referenceRelativePath]; ok {
		return containerTree, nil
	}

	_, containerTree, err := s.loadSymbolTree(ctx, workspaceRoot, referenceRelativePath)
	if err != nil {
		return nil, err
	}
	containerTreeCache[referenceRelativePath] = containerTree

	return containerTree, nil
}

// requestRawDocumentSymbols keeps the wire call thin so symbol shaping stays in stdlsp instead of adapter packages.
func (s *Service) requestRawDocumentSymbols(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
) (string, []json.RawMessage, error) {
	cleanRelativePath, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return "", nil, err
	}

	conn, err := s.config.EnsureConn(ctx, workspaceRoot)
	if err != nil {
		return "", nil, err
	}

	params := &protocol.DocumentSymbolParams{
		WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
		PartialResultParams:    protocol.PartialResultParams{},
		TextDocument: protocol.TextDocumentIdentifier{
			URI: uri.File(absolutePath),
		},
	}

	var rawSymbols []json.RawMessage
	requestDocumentSymbols := func(callCtx context.Context) error {
		return protocol.Call(
			callCtx,
			conn,
			protocol.MethodTextDocumentDocumentSymbol,
			params,
			&rawSymbols,
		)
	}

	var callErr error
	if s.config.OpenFileForDocumentSymbol {
		callErr = s.runRequestWithDocumentContext(ctx, conn, absolutePath, requestDocumentSymbols)
	} else {
		callErr = requestDocumentSymbols(ctx)
	}
	if callErr != nil {
		return "", nil, fmt.Errorf("request document symbols: %w", callErr)
	}

	return cleanRelativePath, rawSymbols, nil
}

// requestReferenceLocations asks the standard LSP server for all non-declaration
// references of the symbol at the given position.
func (s *Service) requestReferenceLocations(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	position protocol.Position,
) ([]protocol.Location, error) {
	cleanRelativePath, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return nil, err
	}

	conn, err := s.config.EnsureConn(ctx, workspaceRoot)
	if err != nil {
		return nil, err
	}

	documentURI := uri.File(absolutePath)
	params := &protocol.ReferenceParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: documentURI},
			Position:     position,
		},
		WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
		PartialResultParams:    protocol.PartialResultParams{},
		Context:                protocol.ReferenceContext{IncludeDeclaration: false},
	}

	var locations []protocol.Location
	referenceRequest := func(callCtx context.Context) error {
		return protocol.Call(callCtx, conn, protocol.MethodTextDocumentReferences, params, &locations)
	}
	if callErr := s.runRequestWithDocumentContext(ctx, conn, absolutePath, referenceRequest); callErr != nil {
		return nil, fmt.Errorf("request references for %q: %w", cleanRelativePath, callErr)
	}

	return locations, nil
}

// runRequestWithDocumentContext lets adapters bracket one request with a temporary open document
// when the language server needs active-buffer context.
func (s *Service) runRequestWithDocumentContext(
	ctx context.Context,
	conn jsonrpc2.Conn,
	absolutePath string,
	run func(context.Context) error,
) error {
	if s.config.WithRequestDocument == nil {
		return run(ctx)
	}

	return s.config.WithRequestDocument(ctx, conn, absolutePath, run)
}

// requestHoverInfo asks the language server for hover text at one symbol definition position.
func (s *Service) requestHoverInfo(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	position protocol.Position,
) (string, error) {
	_, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, relativePath)
	if err != nil {
		return "", err
	}

	conn, err := s.config.EnsureConn(ctx, workspaceRoot)
	if err != nil {
		return "", err
	}

	params := &protocol.HoverParams{
		TextDocumentPositionParams: protocol.TextDocumentPositionParams{
			TextDocument: protocol.TextDocumentIdentifier{URI: uri.File(absolutePath)},
			Position:     position,
		},
		WorkDoneProgressParams: protocol.WorkDoneProgressParams{},
	}

	var hover protocol.Hover
	hoverRequest := func(callCtx context.Context) error {
		return protocol.Call(callCtx, conn, protocol.MethodTextDocumentHover, params, &hover)
	}
	if callErr := s.runRequestWithDocumentContext(ctx, conn, absolutePath, hoverRequest); callErr != nil {
		return "", fmt.Errorf("request hover for %q: %w", relativePath, callErr)
	}

	return strings.TrimSpace(hover.Contents.Value), nil
}

// buildFoundSymbol materializes body and hover metadata for one symbol row on demand.
func (s *Service) buildFoundSymbol(
	ctx context.Context,
	workspaceRoot string,
	node *node,
	request *domain.FindSymbolRequest,
	fileCache map[string]string,
	hoverInfoByKey map[string]string,
) domain.FoundSymbol {
	startLine, endLine := inclusiveLineBounds(node.Range)
	foundSymbol := domain.FoundSymbol{
		Kind:      node.Kind,
		Body:      "",
		Info:      "",
		Path:      node.NamePath,
		File:      node.RelativePath,
		StartLine: startLine,
		EndLine:   endLine,
	}
	if request.IncludeBody {
		body, err := readSymbolBody(workspaceRoot, node, fileCache)
		if err != nil {
			// Body text is optional. One body-read failure must not hide an otherwise valid symbol match.
			slog.WarnContext(
				ctx,
				"skip optional symbol body",
				"err", err,
				"file_path", node.RelativePath,
				"symbol_path", node.NamePath,
			)

			return foundSymbol
		}
		foundSymbol.Body = body

		return foundSymbol
	}
	if !request.IncludeInfo {
		return foundSymbol
	}

	hoverKey := fmt.Sprintf(
		"%s\x00%d\x00%d",
		node.RelativePath,
		node.SelectionRange.Start.Line,
		node.SelectionRange.Start.Character,
	)
	if cachedInfo, ok := hoverInfoByKey[hoverKey]; ok {
		foundSymbol.Info = cachedInfo

		return foundSymbol
	}

	hoverInfo, err := s.requestHoverInfo(ctx, workspaceRoot, node.RelativePath, node.SelectionRange.Start)
	if err != nil {
		// Hover metadata is optional. One hover failure must not hide an otherwise valid symbol match.
		slog.WarnContext(
			ctx,
			"skip optional hover info",
			"err", err,
			"file_path", node.RelativePath,
			"symbol_path", node.NamePath,
		)
		hoverInfoByKey[hoverKey] = ""

		return foundSymbol
	}
	hoverInfoByKey[hoverKey] = hoverInfo
	foundSymbol.Info = hoverInfo

	return foundSymbol
}

// relativePathFromURI converts one file URI into a safe workspace-relative path.
func (s *Service) relativePathFromURI(workspaceRoot string, fileURI uri.URI) (string, error) {
	absolutePath := filepath.Clean(fileURI.Filename())
	normalizedAbsolutePath, err := filepath.EvalSymlinks(absolutePath)
	if err == nil {
		absolutePath = normalizedAbsolutePath
	}

	relativePath, err := filepath.Rel(workspaceRoot, absolutePath)
	if err != nil {
		return "", fmt.Errorf("resolve %q against workspace: %w", absolutePath, err)
	}
	if relativePath == parentDirMarker || strings.HasPrefix(relativePath, parentDirMarker+string(filepath.Separator)) {
		return "", fmt.Errorf("reference path %q points outside the workspace", absolutePath)
	}

	return relativePath, nil
}

// fileFallbackReferenceContainer keeps a raw reference when no symbol contains
// it but the file still belongs to the workspace.
func fileFallbackReferenceContainer(
	workspaceRoot string,
	referenceRelativePath string,
	includeKinds []int,
	excludeKinds []int,
	fileCache map[string]string,
) (domain.SymbolLocation, bool, error) {
	fileKind := int(protocol.SymbolKindFile)
	if !matchesKindFilters(fileKind, includeKinds, excludeKinds) {
		return domain.SymbolLocation{}, false, nil
	}

	// When documentSymbol is empty or does not cover the raw reference position,
	// we still keep the evidence and group it under the file container instead of
	// dropping the reference row completely.
	content, err := readFileContent(workspaceRoot, referenceRelativePath, fileCache)
	if err != nil {
		return domain.SymbolLocation{}, false, err
	}

	lineCount := len(strings.Split(content, "\n"))
	return domain.SymbolLocation{
		Kind:      fileKind,
		Path:      strings.TrimSuffix(path.Base(referenceRelativePath), filepath.Ext(referenceRelativePath)),
		File:      referenceRelativePath,
		StartLine: 0,
		EndLine:   max(0, lineCount-1),
	}, true, nil
}
