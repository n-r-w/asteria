package stdlsp

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/n-r-w/asteria/internal/domain"
	"go.lsp.dev/protocol"
)

// cachedNode stores one JSON-friendly snapshot of the internal symbol-tree node.
type cachedNode struct {
	Kind           int          `json:"kind"`
	NamePath       string       `json:"name_path"`
	RelativePath   string       `json:"relative_path"`
	Range          rangeDTO     `json:"range"`
	SelectionRange rangeDTO     `json:"selection_range"`
	Children       []cachedNode `json:"children"`
}

// positionDTO keeps JSON-friendly protocol positions without importing LSP wire types into the cache package.
type positionDTO struct {
	Line      uint32 `json:"line"`
	Character uint32 `json:"character"`
}

// rangeDTO keeps JSON-friendly protocol ranges without importing LSP wire types into the cache package.
type rangeDTO struct {
	Start positionDTO `json:"start"`
	End   positionDTO `json:"end"`
}

func (s *Service) cacheMetadata(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
) (*SymbolTreeCacheMetadata, bool, error) {
	if s.config.SymbolTreeCache == nil || s.config.BuildSymbolTreeCacheMetadata == nil {
		return nil, false, nil
	}

	metadata, err := s.config.BuildSymbolTreeCacheMetadata(ctx, workspaceRoot, relativePath)
	if err != nil {
		return nil, false, err
	}
	if metadata == nil || !metadata.Enabled {
		if metadata != nil && strings.TrimSpace(metadata.DisabledReason) != "" {
			s.logCacheDisabledOnce(ctx, workspaceRoot, relativePath, metadata)
		}

		return nil, false, nil
	}

	return metadata, true, nil
}

func (s *Service) readSymbolTreeFromCache(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	metadata *SymbolTreeCacheMetadata,
) ([]*node, bool, error) {
	payload, found, err := s.config.SymbolTreeCache.ReadSymbolTree(ctx, &ReadSymbolTreeCacheRequest{
		WorkspaceRoot: workspaceRoot,
		RelativePath:  relativePath,
		Metadata:      *metadata,
	})
	if err != nil || !found {
		return nil, found, err
	}

	return deserializeSymbolTree(payload)
}

func (s *Service) writeSymbolTreeToCache(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	metadata *SymbolTreeCacheMetadata,
	symbolTree []*node,
) {
	payload, err := serializeSymbolTree(symbolTree)
	if err != nil {
		slog.WarnContext(ctx, "serialize symbol tree cache entry", "relative_path", relativePath, "err", err)
		return
	}
	if err = s.config.SymbolTreeCache.WriteSymbolTree(ctx, &WriteSymbolTreeCacheRequest{
		WorkspaceRoot: workspaceRoot,
		RelativePath:  relativePath,
		Metadata:      *metadata,
		Payload:       payload,
	}); err != nil {
		slog.WarnContext(
			ctx,
			"write symbol tree cache entry",
			"adapter_id", metadata.AdapterID,
			"profile_id", metadata.ProfileID,
			"workspace_root", workspaceRoot,
			"relative_path", relativePath,
			"err", err,
		)
	}
}

func (s *Service) logCacheDisabledOnce(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
	metadata *SymbolTreeCacheMetadata,
) {
	warningKey := strings.Join([]string{
		workspaceRoot,
		relativePath,
		metadata.AdapterID,
		metadata.ProfileID,
		metadata.DisabledReason,
	}, "|")
	if _, loaded := s.cacheDisableWarnings.LoadOrStore(warningKey, struct{}{}); loaded {
		return
	}

	slog.WarnContext(
		ctx,
		"disable symbol tree cache",
		"adapter_id", metadata.AdapterID,
		"profile_id", metadata.ProfileID,
		"workspace_root", workspaceRoot,
		"relative_path", relativePath,
		"reason", metadata.DisabledReason,
	)
}

func serializeSymbolTree(symbolTree []*node) ([]byte, error) {
	cachedTree := make([]cachedNode, 0, len(symbolTree))
	for _, treeNode := range symbolTree {
		cachedTree = append(cachedTree, newCachedNode(treeNode))
	}

	return json.Marshal(cachedTree)
}

func deserializeSymbolTree(payload []byte) ([]*node, bool, error) {
	cachedTree, ok := decodeCachedTree(payload)
	if !ok {
		return nil, false, nil
	}

	symbolTree := make([]*node, 0, len(cachedTree))
	for _, cachedTreeNode := range cachedTree {
		cachedTreeNodeCopy := cachedTreeNode
		symbolTree = append(symbolTree, (&cachedTreeNodeCopy).node())
	}

	return symbolTree, true, nil
}

func decodeCachedTree(payload []byte) ([]cachedNode, bool) {
	var cachedTree []cachedNode
	if json.Unmarshal(payload, &cachedTree) != nil {
		return nil, false
	}

	return cachedTree, true
}

func newCachedNode(treeNode *node) cachedNode {
	children := make([]cachedNode, 0, len(treeNode.Children))
	for _, child := range treeNode.Children {
		children = append(children, newCachedNode(child))
	}

	return cachedNode{
		Kind:           treeNode.Kind,
		NamePath:       treeNode.NamePath,
		RelativePath:   treeNode.RelativePath,
		Range:          newRangeDTO(treeNode.Range),
		SelectionRange: newRangeDTO(treeNode.SelectionRange),
		Children:       children,
	}
}

func (n *cachedNode) node() *node {
	children := make([]*node, 0, len(n.Children))
	for _, child := range n.Children {
		childCopy := child
		children = append(children, (&childCopy).node())
	}

	return &node{
		Kind:           n.Kind,
		NamePath:       n.NamePath,
		RelativePath:   n.RelativePath,
		Range:          n.Range.protocolRange(),
		SelectionRange: n.SelectionRange.protocolRange(),
		Children:       children,
	}
}

func newRangeDTO(source protocol.Range) rangeDTO {
	return rangeDTO{
		Start: positionDTO{Line: source.Start.Line, Character: source.Start.Character},
		End:   positionDTO{Line: source.End.Line, Character: source.End.Character},
	}
}

func (r rangeDTO) protocolRange() protocol.Range {
	return protocol.Range{
		Start: protocol.Position{Line: r.Start.Line, Character: r.Start.Character},
		End:   protocol.Position{Line: r.End.Line, Character: r.End.Character},
	}
}

func nodeTreeToOverview(depth int, symbolTree []*node) (domain.GetSymbolsOverviewResult, error) {
	result := domain.GetSymbolsOverviewResult{Symbols: make([]domain.SymbolLocation, 0)}
	appendNodeOverview(&result.Symbols, symbolTree, depth)

	return result, nil
}
