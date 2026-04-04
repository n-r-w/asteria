package stdlsp

import (
	"context"

	"github.com/n-r-w/asteria/internal/domain"
	"go.lsp.dev/jsonrpc2"
	"go.lsp.dev/protocol"
)

// NamePathBuilder constructs the normalized name path used across shared search helpers.
type NamePathBuilder func(parentPath, symbolName string) string

// node keeps one normalized symbol tree node ready for matching and reference resolution.
type node struct {
	// Kind is the stable LSP symbol kind.
	Kind int
	// NamePath is the normalized name path used across searches.
	NamePath string
	// RelativePath is the workspace-relative file path containing the symbol.
	RelativePath string
	// Range is the full body range of the symbol.
	Range protocol.Range
	// SelectionRange points at the defining identifier of the symbol.
	SelectionRange protocol.Range
	// Children keeps the normalized descendants of the symbol.
	Children []*node
}

// searchScope describes which workspace segment should be searched for symbols.
type searchScope struct {
	// RelativePath keeps one normalized workspace key so matching and errors stay stable.
	RelativePath string
	// AbsolutePath keeps disk access separate from the user-facing workspace path.
	AbsolutePath string
	// IsDir lets the traversal reuse one scope shape for files and directories.
	IsDir bool
}

// referenceMatch keeps one resolved reference before grouped transport shaping happens.
type referenceMatch struct {
	// Container is the referencing symbol that contains the reference occurrence.
	Container domain.SymbolLocation
	// Evidence is the concrete representative candidate inside the container symbol.
	Evidence referenceEvidenceCandidate
}

// referenceEvidenceCandidate keeps internal selection data until the final domain evidence is chosen.
type referenceEvidenceCandidate struct {
	// StartLine is the 0-based first line of the representative reference range.
	StartLine int
	// EndLine is the 0-based inclusive last line of the representative reference range.
	EndLine int
	// ContentStartLine is the 0-based first line of the returned snippet.
	ContentStartLine int
	// ContentEndLine is the 0-based inclusive last line of the returned snippet.
	ContentEndLine int
	// Column is the 1-based column number used only for deterministic evidence selection.
	Column int
	// Content is a short source snippet around the representative reference.
	Content string
}

// EnsureConnFunc returns one live JSON-RPC connection that is ready for standard LSP requests for one root.
type EnsureConnFunc func(ctx context.Context, workspaceRoot string) (jsonrpc2.Conn, error)

// WithRequestDocumentFunc lets an adapter prepare one file for one standard-LSP request that needs
// an active editor buffer in the current session.
type WithRequestDocumentFunc func(
	ctx context.Context,
	conn jsonrpc2.Conn,
	absolutePath string,
	run func(context.Context) error,
) error

// IgnoreDirFunc decides whether one workspace-relative directory should be skipped during scope traversal.
type IgnoreDirFunc func(relativePath string) bool

// BuildSymbolTreeCacheMetadataFunc tells stdlsp whether one profile may cache one file's symbol tree.
type BuildSymbolTreeCacheMetadataFunc func(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
) (*SymbolTreeCacheMetadata, error)

// ISymbolTreeCache stores serialized per-file symbol trees outside the current request lifetime.
type ISymbolTreeCache interface {
	ReadSymbolTree(ctx context.Context, request *ReadSymbolTreeCacheRequest) ([]byte, bool, error)
	WriteSymbolTree(ctx context.Context, request *WriteSymbolTreeCacheRequest) error
}

// SymbolTreeCacheMetadata describes the adapter-controlled cache key inputs for one symbol-tree profile.
type SymbolTreeCacheMetadata struct {
	// Enabled reports whether disk cache may be used for the current adapter profile.
	Enabled bool
	// DisabledReason explains why disk cache is unavailable for logging and diagnostics.
	DisabledReason string
	// AdapterID isolates entries for different adapters that can search the same workspace.
	AdapterID string
	// ProfileID isolates entries for different symbol-tree modes of the same adapter.
	ProfileID string
	// AdapterFingerprint invalidates entries when adapter/runtime behavior changes.
	AdapterFingerprint string
	// AdditionalDependencies lists workspace-relative files that also influence the cached symbol tree.
	AdditionalDependencies []string
}

// ReadSymbolTreeCacheRequest describes one symbol-tree cache lookup from stdlsp into the storage layer.
type ReadSymbolTreeCacheRequest struct {
	// WorkspaceRoot keeps the normalized absolute workspace root used for namespacing and dependency checks.
	WorkspaceRoot string
	// RelativePath keeps the workspace-relative path of the file whose symbol tree is requested.
	RelativePath string
	// Metadata keeps the adapter-owned cache-key inputs for the current profile.
	Metadata SymbolTreeCacheMetadata
}

// WriteSymbolTreeCacheRequest describes one symbol-tree cache write from stdlsp into the storage layer.
type WriteSymbolTreeCacheRequest struct {
	// WorkspaceRoot keeps the normalized absolute workspace root used for namespacing and dependency checks.
	WorkspaceRoot string
	// RelativePath keeps the workspace-relative path of the file whose symbol tree is being cached.
	RelativePath string
	// Metadata keeps the adapter-owned cache-key inputs for the current profile.
	Metadata SymbolTreeCacheMetadata
	// Payload keeps the serialized symbol-tree artifact to persist.
	Payload []byte
}

// Config describes the adapter-specific hooks that stdlsp needs to coordinate standard LSP workflows.
type Config struct {
	// Extensions lists the file extensions this adapter can search.
	Extensions []string
	// EnsureConn returns one live connection ready to serve requests for the given workspace root.
	EnsureConn EnsureConnFunc
	// WithRequestDocument temporarily opens one file in the current LSP session for the wrapped call.
	WithRequestDocument WithRequestDocumentFunc
	// OpenFileForDocumentSymbol lets adapters opt into temporary didOpen/didClose around documentSymbol.
	OpenFileForDocumentSymbol bool
	// OpenFileForReferenceWorkflow lets adapters keep the target file open for the whole references workflow.
	OpenFileForReferenceWorkflow bool
	// BuildNamePath optionally normalizes adapter-specific symbol naming into the shared name-path format.
	BuildNamePath NamePathBuilder
	// IgnoreDir optionally skips workspace-relative directories during directory traversal.
	IgnoreDir IgnoreDirFunc
	// SymbolTreeCache optionally stores serialized per-file symbol trees across requests.
	SymbolTreeCache ISymbolTreeCache
	// BuildSymbolTreeCacheMetadata optionally decides whether one file may use disk cache and how to key it.
	BuildSymbolTreeCacheMetadata BuildSymbolTreeCacheMetadataFunc
}
