// Package domain contains shared domain models and constants used across the application.
package domain

import (
	"errors"
	"fmt"
	"strings"

	lspprotocol "go.lsp.dev/protocol"
)

// SymbolLocation describes one symbol match location in repository files.
type SymbolLocation struct {
	// Kind is the stable LSP symbol kind code for this symbol.
	Kind int
	// Path is the slash-delimited symbol path. Duplicate same-name siblings add '@line:character' to the last segment.
	Path string
	// File is the workspace-relative file path containing the symbol.
	File string
	// StartLine is the 0-based first line of the symbol range.
	StartLine int
	// EndLine is the 0-based inclusive last line of the symbol range.
	EndLine int
}

// GetSymbolsOverviewFilter groups all filter parameters for symbols overview operation.
type GetSymbolsOverviewFilter struct {
	// Depth limits how deep child symbols should be returned.
	Depth int
}

// Validate checks that the symbols overview filter contains consistent parameters.
func (r *GetSymbolsOverviewFilter) Validate() error {
	if r.Depth < 0 {
		return errors.New("depth must be non-negative")
	}

	return nil
}

// GetSymbolsOverviewRequest is input model for symbols overview operation.
type GetSymbolsOverviewRequest struct {
	GetSymbolsOverviewFilter

	// WorkspaceRoot selects the workspace root used to resolve File.
	WorkspaceRoot string
	// File is the workspace-relative file path to inspect.
	File string
}

// Validate checks that the overview request contains all required fields.
func (r *GetSymbolsOverviewRequest) Validate() error {
	var err error
	if strings.TrimSpace(r.WorkspaceRoot) == "" {
		err = errors.Join(err, errors.New("workspace_root is required"))
	}
	if strings.TrimSpace(r.File) == "" {
		err = errors.Join(err, errors.New("file is required"))
	}
	return errors.Join(err, r.GetSymbolsOverviewFilter.Validate())
}

// GetSymbolsOverviewResult is output model for symbols overview operation.
type GetSymbolsOverviewResult struct {
	// Symbols contains the overview symbols in one stable flat list.
	Symbols []SymbolLocation
}

// FindSymbolFilter groups all filter parameters for symbol lookup operation.
type FindSymbolFilter struct {
	// Path is the requested symbol path pattern. Duplicate same-name siblings add '@line:character' to the last segment.
	Path string
	// IncludeKinds restricts results to the provided symbol kinds.
	IncludeKinds []int
	// ExcludeKinds removes results matching the provided symbol kinds.
	ExcludeKinds []int
	// Depth limits how deep child symbols should be returned.
	Depth int
	// IncludeBody requests symbol body/source inclusion when supported.
	IncludeBody bool
	// IncludeInfo requests symbol metadata inclusion when supported.
	IncludeInfo bool
	// SubstringMatching enables partial matching for the last path segment.
	SubstringMatching bool
}

// Validate checks that the symbol lookup filter contains consistent search parameters.
func (r *FindSymbolFilter) Validate() error {
	var err error
	if strings.TrimSpace(r.Path) == "" {
		err = errors.Join(err, errors.New("path is required"))
	}
	if r.Depth < 0 {
		err = errors.Join(err, errors.New("depth must be non-negative"))
	}
	for _, kind := range r.IncludeKinds {
		if kindErr := validateLSPKind(kind); kindErr != nil {
			err = errors.Join(err, fmt.Errorf("include_kinds contains %w", kindErr))
		}
	}
	for _, kind := range r.ExcludeKinds {
		if kindErr := validateLSPKind(kind); kindErr != nil {
			err = errors.Join(err, fmt.Errorf("exclude_kinds contains %w", kindErr))
		}
	}

	return err
}

// FindSymbolRequest is input model for symbol lookup operation.
type FindSymbolRequest struct {
	FindSymbolFilter

	// WorkspaceRoot selects the workspace root used to resolve Scope.
	WorkspaceRoot string
	// Scope optionally narrows search scope to one workspace-relative file or directory.
	Scope string
}

// Validate checks that the symbol lookup request contains consistent search parameters.
func (r *FindSymbolRequest) Validate() error {
	var err error
	if strings.TrimSpace(r.WorkspaceRoot) == "" {
		err = errors.Join(err, errors.New("workspace_root is required"))
	}

	return errors.Join(err, r.FindSymbolFilter.Validate())
}

// FindSymbolResult is output model for symbol lookup operation.
type FindSymbolResult struct {
	// Symbols contains the matched symbol locations.
	Symbols []FoundSymbol
}

// FoundSymbol describes a symbol found by the FindSymbol operation.
type FoundSymbol struct {
	// Kind is the stable LSP symbol kind code for this symbol.
	Kind int
	// Body is the source code of the symbol, if requested.
	Body string
	// Info is the metadata of the symbol, if requested.
	Info string
	// Path is the slash-delimited symbol path. Duplicate same-name siblings add '@line:character' to the last segment.
	Path string
	// File is the workspace-relative file path containing the symbol.
	File string
	// StartLine is the 0-based first line of the symbol range.
	StartLine int
	// EndLine is the 0-based inclusive last line of the symbol range.
	EndLine int
}

// ReferencingSymbol groups unique reference lines by the symbol that contains them.
type ReferencingSymbol struct {
	// Kind is the stable LSP symbol kind code for the referencing symbol.
	Kind int
	// Path is the slash-delimited symbol path. Duplicate same-name siblings add '@line:character' to the last segment.
	Path string
	// File is the workspace-relative file path containing the symbol.
	File string
	// ContentStartLine is the 0-based first line of the representative code snippet.
	ContentStartLine int
	// ContentEndLine is the 0-based inclusive last line of the representative code snippet.
	ContentEndLine int
	// Content is a short source snippet around the representative reference.
	Content string
}

// FindReferencingSymbolsFilter groups all filter parameters for symbol references operation.
type FindReferencingSymbolsFilter struct {
	// Path identifies the target symbol. Duplicate same-name siblings add '@line:character' to the last segment.
	Path string
	// IncludeKinds restricts returned referencing symbols to the provided kinds.
	IncludeKinds []int
	// ExcludeKinds removes returned referencing symbols matching the provided kinds.
	ExcludeKinds []int
}

// Validate checks that the symbol references filter contains consistent search parameters.
func (r *FindReferencingSymbolsFilter) Validate() error {
	var err error
	if strings.TrimSpace(r.Path) == "" {
		err = errors.Join(err, errors.New("path is required"))
	}
	for _, kind := range r.IncludeKinds {
		if kindErr := validateLSPKind(kind); kindErr != nil {
			err = errors.Join(err, fmt.Errorf("include_kinds contains %w", kindErr))
		}
	}
	for _, kind := range r.ExcludeKinds {
		if kindErr := validateLSPKind(kind); kindErr != nil {
			err = errors.Join(err, fmt.Errorf("exclude_kinds contains %w", kindErr))
		}
	}

	return err
}

// FindReferencingSymbolsRequest is input model for symbol references operation.
type FindReferencingSymbolsRequest struct {
	FindReferencingSymbolsFilter

	// WorkspaceRoot selects the workspace root used to resolve File.
	WorkspaceRoot string
	// File is the workspace-relative file path containing the target symbol.
	File string
}

// Validate checks that the reference lookup request identifies one concrete target symbol and valid kind filters.
func (r *FindReferencingSymbolsRequest) Validate() error {
	var err error
	if strings.TrimSpace(r.WorkspaceRoot) == "" {
		err = errors.Join(err, errors.New("workspace_root is required"))
	}
	if strings.TrimSpace(r.File) == "" {
		err = errors.Join(err, errors.New("file is required"))
	}

	return errors.Join(err, r.FindReferencingSymbolsFilter.Validate())
}

// FindReferencingSymbolsResult is output model for symbol references operation.
type FindReferencingSymbolsResult struct {
	// Symbols contains referencing symbols grouped with their unique reference lines.
	Symbols []ReferencingSymbol
}

// validateLSPKind checks if the provided symbol kind is a valid LSP SymbolKind.
func validateLSPKind(kind int) error {
	if kind < int(lspprotocol.SymbolKindFile) || kind > int(lspprotocol.SymbolKindTypeParameter) {
		return fmt.Errorf("invalid symbol kind (allowed 1-26): %d", kind)
	}

	return nil
}
