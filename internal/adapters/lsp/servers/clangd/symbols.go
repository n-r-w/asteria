package lspclangd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/domain"
	"go.lsp.dev/protocol"
)

// compileCommandEntry keeps one compilation database row editable while managed copies are materialized.
type compileCommandEntry struct {
	Directory string   `json:"directory"`
	File      string   `json:"file"`
	Arguments []string `json:"arguments,omitempty"`
	Command   string   `json:"command,omitempty"`
	Output    string   `json:"output,omitempty"`
}

// GetSymbolsOverview delegates the standard document-symbol workflow to stdlsp.
func (s *Service) GetSymbolsOverview(
	ctx context.Context,
	request *domain.GetSymbolsOverviewRequest,
) (domain.GetSymbolsOverviewResult, error) {
	if request != nil {
		if err := s.prepareWorkspaceCache(request.WorkspaceRoot); err != nil {
			return domain.GetSymbolsOverviewResult{}, err
		}
	}

	return s.std.GetSymbolsOverview(ctx, request)
}

// FindSymbol delegates canonical path matching to the shared standard-LSP search flow.
func (s *Service) FindSymbol(
	ctx context.Context,
	request *domain.FindSymbolRequest,
) (domain.FindSymbolResult, error) {
	if request != nil {
		if err := s.prepareWorkspaceCache(request.WorkspaceRoot); err != nil {
			return domain.FindSymbolResult{}, err
		}
	}

	return s.std.FindSymbol(ctx, request)
}

// FindReferencingSymbols keeps the target-directory file set open for the whole shared workflow so clangd can
// resolve cross-file references before stdlsp groups the final result.
func (s *Service) FindReferencingSymbols(
	ctx context.Context,
	request *domain.FindReferencingSymbolsRequest,
) (domain.FindReferencingSymbolsResult, error) {
	if request == nil {
		return domain.FindReferencingSymbolsResult{}, errors.New("request is required")
	}
	if err := request.Validate(); err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}
	if err := s.prepareWorkspaceCache(request.WorkspaceRoot); err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	workspaceRoot, err := helpers.ResolveWorkspaceRoot(request.WorkspaceRoot)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	referenceWorkflowFiles, err := helpers.CollectReferenceWorkflowFiles(
		workspaceRoot,
		request.File,
		extensions,
		shouldIgnoreDir,
	)
	if requiresWorkspaceWideReferenceWorkflow(request.File) {
		referenceWorkflowFiles, err = collectWorkspaceWideReferenceWorkflowFiles(
			workspaceRoot,
			request.File,
			extensions,
			shouldIgnoreDir,
		)
	}
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	conn, err := s.rt.EnsureConn(ctx, workspaceRoot)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	var result domain.FindReferencingSymbolsResult
	err = helpers.RunWithReferenceWorkflowFiles(
		ctx,
		conn,
		workspaceRoot,
		referenceWorkflowFiles,
		s.withRequestDocument,
		func(callCtx context.Context) error {
			var callErr error
			result, callErr = s.std.FindReferencingSymbols(callCtx, request)

			return callErr
		},
	)
	if err != nil {
		return domain.FindReferencingSymbolsResult{}, err
	}

	return result, nil
}

// requiresWorkspaceWideReferenceWorkflow broadens the temporary open-file set
// for declarations that act as cross-translation-unit interfaces.
func requiresWorkspaceWideReferenceWorkflow(relativePath string) bool {
	switch strings.ToLower(filepath.Ext(relativePath)) {
	case ".h", ".hh", ".hpp", ".hxx", ".ccm", ".cppm", ".cxxm", ".c++m":
		return true
	default:
		return false
	}
}

// collectWorkspaceWideReferenceWorkflowFiles keeps interface-file reference
// lookups stable by opening all supported workspace files while still leaving
// the target file last in the request sequence.
func collectWorkspaceWideReferenceWorkflowFiles(
	workspaceRoot string,
	targetRelativePath string,
	extensions []string,
	ignoreDir func(relativePath string) bool,
) ([]string, error) {
	cleanTargetRelativePath, _, err := helpers.ResolveDocumentPath(workspaceRoot, targetRelativePath)
	if err != nil {
		return nil, err
	}

	referenceWorkflowFiles, err := helpers.CollectDirectoryFiles(
		workspaceRoot,
		workspaceRoot,
		extensions,
		ignoreDir,
	)
	if err != nil {
		return nil, err
	}

	workflowFiles := make([]string, 0, len(referenceWorkflowFiles))
	for _, relativePath := range referenceWorkflowFiles {
		if relativePath != cleanTargetRelativePath {
			workflowFiles = append(workflowFiles, relativePath)
		}
	}

	return append(workflowFiles, cleanTargetRelativePath), nil
}

// patchInitializeParams points clangd at the managed compilation database directory
// before the session handshake starts.
func (s *Service) patchInitializeParams(workspaceRoot string, params *protocol.InitializeParams) error {
	cacheDir, err := s.cacheDir(workspaceRoot)
	if err != nil {
		return err
	}
	hasDatabase, err := prepareManagedCompilationDatabase(workspaceRoot, cacheDir)
	if err != nil {
		return err
	}
	if !hasDatabase {
		return nil
	}

	params.InitializationOptions = map[string]any{
		clangdCompilationDatabasePathKey: cacheDir,
	}

	return nil
}

// cacheDir returns the managed clangd cache directory for one workspace root.
func (s *Service) cacheDir(workspaceRoot string) (string, error) {
	return helpers.AdapterCacheDir(s.cacheRoot, workspaceRoot, clangdAdapterName)
}

// prepareWorkspaceCache refreshes the managed compile database copy before the current tool request runs.
func (s *Service) prepareWorkspaceCache(workspaceRoot string) error {
	cacheDir, err := s.cacheDir(workspaceRoot)
	if err != nil {
		return err
	}

	_, err = prepareManagedCompilationDatabase(workspaceRoot, cacheDir)

	return err
}

// shouldIgnoreDir skips hidden directories so request-scoped clangd file opening stays focused on project sources.
func shouldIgnoreDir(relativePath string) bool {
	return strings.HasPrefix(filepath.Base(relativePath), ".")
}

// shouldWatchClangdFile keeps runtime-managed watched-files focused on supported
// C-family sources and compile config files.
func shouldWatchClangdFile(relativePath string) bool {
	baseName := filepath.Base(relativePath)
	if baseName == compileCommandsFileName || baseName == compileFlagsFileName {
		return true
	}

	extension := filepath.Ext(relativePath)
	for _, supportedExtension := range extensions {
		if strings.EqualFold(extension, supportedExtension) {
			return true
		}
	}

	return false
}

// prepareManagedCompilationDatabase refreshes the managed compile database copy
// that clangd reads from its cache directory.
func prepareManagedCompilationDatabase(workspaceRoot, cacheDir string) (bool, error) {
	normalizedWorkspaceRoot, err := helpers.ResolveWorkspaceRoot(workspaceRoot)
	if err != nil {
		return false, err
	}
	if err = os.MkdirAll(cacheDir, clangdCacheDirPermissions); err != nil {
		return false, fmt.Errorf("create clangd cache directory: %w", err)
	}
	if cleanupErr := cleanupManagedCompilationDatabase(cacheDir); cleanupErr != nil {
		return false, cleanupErr
	}

	compileCommandsPath := filepath.Join(normalizedWorkspaceRoot, compileCommandsFileName)
	if fileExists(compileCommandsPath) {
		if materializeErr := materializeCompileCommands(
			normalizedWorkspaceRoot,
			compileCommandsPath,
			filepath.Join(cacheDir, compileCommandsFileName),
		); materializeErr != nil {
			return false, materializeErr
		}

		return true, nil
	}

	compileFlagsPath := filepath.Join(normalizedWorkspaceRoot, compileFlagsFileName)
	if fileExists(compileFlagsPath) {
		content, readErr := os.ReadFile(filepath.Clean(compileFlagsPath))
		if readErr != nil {
			return false, fmt.Errorf("read compile flags: %w", readErr)
		}
		if writeErr := os.WriteFile(
			filepath.Join(cacheDir, compileFlagsFileName),
			content,
			clangdManagedFilePermissions,
		); writeErr != nil {
			return false, fmt.Errorf("write managed compile flags: %w", writeErr)
		}

		return true, nil
	}

	return false, nil
}

// cleanupManagedCompilationDatabase removes stale managed compile database files
// before a fresh primary source is copied.
func cleanupManagedCompilationDatabase(cacheDir string) error {
	for _, fileName := range []string{compileCommandsFileName, compileFlagsFileName} {
		filePath := filepath.Join(cacheDir, fileName)
		if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale managed %s: %w", fileName, err)
		}
	}

	return nil
}

// materializeCompileCommands rewrites relative command directories into absolute
// workspace paths before clangd reads them.
func materializeCompileCommands(workspaceRoot, sourcePath, targetPath string) error {
	content, err := os.ReadFile(filepath.Clean(sourcePath))
	if err != nil {
		return fmt.Errorf("read compile commands: %w", err)
	}

	var entries []compileCommandEntry
	if err = json.Unmarshal(content, &entries); err != nil {
		return fmt.Errorf("parse compile commands: %w", err)
	}

	for i := range entries {
		if filepath.IsAbs(entries[i].Directory) {
			continue
		}

		entries[i].Directory = filepath.Clean(filepath.Join(workspaceRoot, entries[i].Directory))
	}

	encodedEntries, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode managed compile commands: %w", err)
	}
	if err = os.WriteFile(targetPath, encodedEntries, clangdManagedFilePermissions); err != nil {
		return fmt.Errorf("write managed compile commands: %w", err)
	}

	return nil
}

// fileExists keeps compile database source probing readable while ignoring non-existent paths.
func fileExists(path string) bool {
	fileInfo, err := os.Stat(filepath.Clean(path))
	if err != nil {
		return false
	}

	return !fileInfo.IsDir()
}
