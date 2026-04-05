// Package router provides the main application logic for the Asteria MCP server,
// including request routing and handler management.
package router

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/samber/lo"

	"github.com/n-r-w/asteria/internal/domain"
	"github.com/n-r-w/asteria/internal/server"
)

// Service implements the main use-case router that dispatches requests to language-specific LSP implementations.
type Service struct {
	lspByExtension map[string]ILSP
}

// foundSymbolKey keeps in-memory dedupe keys typed and allocation-free.
type foundSymbolKey struct {
	Path      string
	File      string
	StartLine int
	EndLine   int
}

var _ server.ILSP = (*Service)(nil)

// New creates a new Service with the provided LSP implementations.
func New(lsps []ILSP) (*Service, error) {
	lspByExtension := make(map[string]ILSP)
	for _, lsp := range lsps {
		for _, ext := range lsp.Extensions() {
			if ext == "" {
				return nil, fmt.Errorf("lsp implementation %T has empty extension", lsp)
			}
			normalizedExt := strings.ToLower(ext)
			if existingLSP, exists := lspByExtension[normalizedExt]; exists {
				if existingLSP == lsp {
					continue
				}

				return nil, fmt.Errorf("multiple lsp implementations support extension %q: %T and %T", ext, existingLSP, lsp)
			}

			lspByExtension[normalizedExt] = lsp
		}
	}

	return &Service{
		lspByExtension: lspByExtension,
	}, nil
}

// GetSymbolsOverview returns a high-level overview of symbols in a file.
func (s *Service) GetSymbolsOverview(
	ctx context.Context, request *domain.GetSymbolsOverviewRequest,
) (domain.GetSymbolsOverviewResult, error) {
	effectiveRoot, lsp, err := s.resolveFileScopedLSP(request.WorkspaceRoot, request.File, "file_path")
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, err
	}
	request.WorkspaceRoot = effectiveRoot

	// Get the symbols overview from LSP
	result, err := lsp.GetSymbolsOverview(ctx, request)
	if err != nil {
		return domain.GetSymbolsOverviewResult{}, sanitizePublicError(
			err,
			fmt.Sprintf("lsp %T failed to get symbols overview", lsp),
		)
	}

	return result, nil
}

// FindSymbol finds symbols matching the pattern.
func (s *Service) FindSymbol(
	ctx context.Context, request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	var results []domain.FoundSymbol
	effectiveRoot, err := normalizeWorkspaceRoot(request.WorkspaceRoot)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}

	// search for files matching the request path
	fileNames, isSingleFileScope, err := s.resolveSearchFiles(effectiveRoot, request.Scope)
	if err != nil {
		return domain.FindSymbolResult{}, err
	}
	trimmedScope := strings.TrimSpace(request.Scope)
	// A single-file scope must stay file-scoped so the router can reject unsupported extensions early and keep
	// the adapter request anchored to one validated workspace-relative file path.
	if isSingleFileScope {
		for _, fileName := range fileNames {
			lspsForFile := s.getLSPsForFiles([]string{fileName})
			if len(lspsForFile) == 0 {
				return domain.FindSymbolResult{}, domain.NewUnsupportedExtensionError(
					strings.ToLower(filepath.Ext(fileName)),
				)
			}

			relativeFilePath, relErr := filepath.Rel(effectiveRoot, fileName)
			if relErr != nil {
				return domain.FindSymbolResult{}, domain.NewInternalError(relErr)
			}
			// Adapter requests use workspace-relative slash paths even on Windows to keep routing stable.
			relativeFilePath = filepath.ToSlash(relativeFilePath)

			fileRequest := &domain.FindSymbolRequest{
				FindSymbolFilter: request.FindSymbolFilter,
				WorkspaceRoot:    effectiveRoot,
				Scope:            relativeFilePath,
			}

			result, findErr := lspsForFile[0].FindSymbol(ctx, fileRequest)
			if findErr != nil {
				return domain.FindSymbolResult{}, sanitizePublicError(
					findErr,
					fmt.Sprintf("lsp %T failed to find symbol", lspsForFile[0]),
				)
			}

			results = mergeFoundSymbols(results, result.Symbols)
		}

		return domain.FindSymbolResult{Symbols: results}, nil
	}

	// Directory and workspace scopes must fan out once per matching adapter, not once per file, because each
	// adapter owns its own traversal rules such as ignored directories and language-specific search semantics.
	for _, lsp := range s.getLSPsForFiles(fileNames) {
		searchRequest := &domain.FindSymbolRequest{
			FindSymbolFilter: request.FindSymbolFilter,
			WorkspaceRoot:    effectiveRoot,
			Scope:            trimmedScope,
		}

		result, findErr := lsp.FindSymbol(ctx, searchRequest)
		if findErr != nil {
			return domain.FindSymbolResult{}, sanitizePublicError(
				findErr,
				fmt.Sprintf("lsp %T failed to find symbol", lsp),
			)
		}

		results = mergeFoundSymbols(results, result.Symbols)
	}

	return domain.FindSymbolResult{Symbols: results}, nil
}

// FindReferencingSymbols returns non-declaration references for a target symbol.
func (s *Service) FindReferencingSymbols(
	ctx context.Context, request *domain.FindReferencingSymbolsRequest,
) (domain.FindReferencingSymbolsResult, error) {
	effectiveRoot, lsp, err := s.resolveFileScopedLSP(request.WorkspaceRoot, request.File, "file_path")
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}
	request.WorkspaceRoot = effectiveRoot

	result, err := lsp.FindReferencingSymbols(ctx, request)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, sanitizePublicError(
			err,
			fmt.Sprintf("lsp %T failed to find referencing symbols", lsp),
		)
	}

	return result, nil
}

// resolveFileScopedLSP validates one file-scoped request and returns the matching LSP for that file.
func (s *Service) resolveFileScopedLSP(
	workspaceRoot string,
	relativeFilePath string,
	argName string,
) (effectiveRoot string, lsp ILSP, err error) {
	effectiveRoot, err = normalizeWorkspaceRoot(workspaceRoot)
	if err != nil {
		return "", nil, err
	}

	filePath, err := s.getAbsoluteFilePath(effectiveRoot, relativeFilePath, argName)
	if err != nil {
		return "", nil, err
	}

	lsps := s.getLSPsForFiles([]string{filePath})
	if len(lsps) == 0 {
		return "", nil, domain.NewUnsupportedExtensionError(strings.ToLower(filepath.Ext(filePath)))
	}

	return effectiveRoot, lsps[0], nil
}

// resolveSearchFiles expands a request path into concrete file paths and reports whether it targets one file.
func (s *Service) resolveSearchFiles(
	workspaceRoot string,
	relativePath string,
) (fileNames []string, isSingleFileScope bool, err error) {
	trimmedRelativePath := strings.TrimSpace(relativePath)
	searchRootPath, err := s.getAbsolutePath(workspaceRoot, trimmedRelativePath, "scope_path")
	if err != nil {
		return nil, false, err
	}

	fileNames, isSingleFileScope, err = s.getFilesForPath(searchRootPath, trimmedRelativePath)
	if err != nil {
		return nil, false, err
	}

	return fileNames, isSingleFileScope, nil
}

// getFilesForPath returns absolute file paths matching the provided absolute path, which can be a file or a directory.
func (s *Service) getFilesForPath(
	searchRootPath, requestedScopePath string,
) (fileNames []string, isSingleFileScope bool, err error) {
	fileInfo, err := os.Stat(searchRootPath)
	if err != nil {
		return nil, false, sanitizePathAccessError("scope_path", requestedScopePath, err)
	}
	if !fileInfo.IsDir() {
		return []string{searchRootPath}, true, nil
	}

	var files []string
	err = filepath.WalkDir(searchRootPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, false, domain.NewSafeError(
			domain.NewPathReadError("scope_path", requestedScopePath, err).Error(),
			err,
		)
	}
	return files, false, nil
}

// getLSPsForFiles selects only LSPs needed for the provided file list.
func (s *Service) getLSPsForFiles(fileNames []string) []ILSP {
	matchedExtensions := s.collectMatchedExtensions(fileNames)

	return s.selectLSPsByExtensions(matchedExtensions)
}

// collectMatchedExtensions records only extensions supported by configured LSPs.
func (s *Service) collectMatchedExtensions(fileNames []string) map[string]struct{} {
	matchedExtensions := make(map[string]struct{})
	for _, file := range fileNames {
		extension := strings.ToLower(filepath.Ext(file))
		if extension == "" {
			continue
		}
		if _, ok := s.lspByExtension[extension]; ok {
			matchedExtensions[extension] = struct{}{}
		}
	}

	return matchedExtensions
}

// selectLSPsByExtensions returns only LSPs needed for the extensions discovered in one search scope.
func (s *Service) selectLSPsByExtensions(matchedExtensions map[string]struct{}) []ILSP {
	selectedLSPSet := make(map[ILSP]struct{}, len(matchedExtensions))
	for extension := range matchedExtensions {
		lsp, ok := s.lspByExtension[extension]
		if !ok {
			continue
		}

		selectedLSPSet[lsp] = struct{}{}
	}

	return lo.Keys(selectedLSPSet)
}

// getAbsoluteFilePath resolves one workspace-relative path and rejects directories before extension-based routing.
func (s *Service) getAbsoluteFilePath(workspaceRoot, relativeFilePath, argName string) (string, error) {
	absoluteFilePath, err := s.getAbsolutePath(workspaceRoot, relativeFilePath, argName)
	if err != nil {
		return "", err
	}

	fileInfo, err := os.Stat(absoluteFilePath)
	if err != nil {
		return "", sanitizePathAccessError(argName, relativeFilePath, err)
	}
	if fileInfo.IsDir() {
		return "", domain.NewPathPointsToDirectoryError(argName, relativeFilePath)
	}

	if filepath.Ext(absoluteFilePath) == "" {
		return "", domain.NewPathMustPointToFileWithExtensionError(argName, relativeFilePath)
	}

	return absoluteFilePath, nil
}

// getAbsolutePath resolves one workspace-relative path and keeps callers inside the workspace root.
func (s *Service) getAbsolutePath(workspaceRoot, relativePath, argName string) (string, error) {
	if filepath.IsAbs(relativePath) {
		return "", domain.NewPathMustBeWorkspaceRelativeError(argName, relativePath)
	}

	absolutePath := filepath.Clean(filepath.Join(workspaceRoot, relativePath))
	relativeToWorkspace, err := filepath.Rel(workspaceRoot, absolutePath)
	if err != nil {
		return "", domain.NewInternalError(err)
	}

	parentPrefix := ".." + string(filepath.Separator)
	if relativeToWorkspace == ".." || strings.HasPrefix(relativeToWorkspace, parentPrefix) {
		return "", domain.NewPathEscapesWorkspaceRootError(argName, relativePath)
	}

	return absolutePath, nil
}

// normalizeWorkspaceRoot validates and canonicalizes one workspace root for filesystem routing.
func normalizeWorkspaceRoot(workspaceRoot string) (string, error) {
	trimmedWorkspaceRoot := strings.TrimSpace(workspaceRoot)
	if trimmedWorkspaceRoot == "" {
		return "", domain.NewSafeError("workspace_root is required", nil)
	}
	if !filepath.IsAbs(trimmedWorkspaceRoot) {
		return "", domain.NewPathMustBeAbsoluteError("workspace_root", trimmedWorkspaceRoot)
	}

	cleanWorkspaceRoot := filepath.Clean(trimmedWorkspaceRoot)
	normalizedWorkspaceRoot, err := filepath.EvalSymlinks(cleanWorkspaceRoot)
	if err != nil {
		return "", sanitizePathAccessError("workspace_root", cleanWorkspaceRoot, err)
	}

	fileInfo, err := os.Stat(normalizedWorkspaceRoot)
	if err != nil {
		return "", sanitizePathAccessError("workspace_root", cleanWorkspaceRoot, err)
	}
	if !fileInfo.IsDir() {
		return "", domain.NewPathMustPointToDirectoryError("workspace_root", cleanWorkspaceRoot)
	}

	return normalizedWorkspaceRoot, nil
}

// sanitizePublicError preserves explicit safe errors and turns uncategorized LSP startup timeouts into one public hint.
func sanitizePublicError(err error, internalMessage string) error {
	if safeErr, ok := errors.AsType[*domain.SafeError](err); ok {
		return safeErr
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return domain.NewSafeError(
			timeoutWarmupPublicMessage,
			fmt.Errorf("%s: %w", internalMessage, err),
		)
	}

	return domain.NewInternalError(fmt.Errorf("%s: %w", internalMessage, err))
}

// sanitizePathAccessError maps filesystem errors to public-safe path validation messages.
func sanitizePathAccessError(argName, path string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return domain.NewPathNotFoundError(argName, path, err)
	}

	return domain.NewPathAccessError(argName, path, err)
}

// mergeFoundSymbols deduplicates broad search results while keeping the first-seen order stable.
func mergeFoundSymbols(
	existing []domain.FoundSymbol,
	additions []domain.FoundSymbol,
) []domain.FoundSymbol {
	existingIndexByKey := make(map[foundSymbolKey]int)
	for idx, symbol := range existing {
		symbolKey := foundSymbolKey{
			Path:      symbol.Path,
			File:      symbol.File,
			StartLine: symbol.StartLine,
			EndLine:   symbol.EndLine,
		}
		existingIndexByKey[symbolKey] = idx
	}

	for _, symbol := range additions {
		symbolKey := foundSymbolKey{
			Path:      symbol.Path,
			File:      symbol.File,
			StartLine: symbol.StartLine,
			EndLine:   symbol.EndLine,
		}
		if existingIndex, ok := existingIndexByKey[symbolKey]; ok {
			if existing[existingIndex].Body == "" {
				existing[existingIndex].Body = symbol.Body
			}
			if existing[existingIndex].Info == "" {
				existing[existingIndex].Info = symbol.Info
			}

			continue
		}

		existingIndexByKey[symbolKey] = len(existing)
		existing = append(existing, symbol)
	}

	return existing
}
