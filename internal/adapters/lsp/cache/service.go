package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/helpers"
	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
)

// Service stores serialized symbol-tree artifacts under the configured cache root.
type Service struct {
	root string
}

var _ stdlsp.ISymbolTreeCache = (*Service)(nil)

// New validates the cache root once so future reads and writes can stay focused on artifact storage.
func New(cacheRoot string) (*Service, error) {
	normalizedCacheRoot, err := helpers.ResolveCacheRoot(cacheRoot)
	if err != nil {
		return nil, err
	}

	return &Service{root: normalizedCacheRoot}, nil
}

// ReadSymbolTree returns one cached symbol-tree payload when the on-disk entry exactly matches the current inputs.
func (s *Service) ReadSymbolTree(
	_ context.Context,
	request *stdlsp.ReadSymbolTreeCacheRequest,
) (payload []byte, found bool, err error) {
	preparedRequest, ok := s.prepareReadRequest(request)
	if !ok {
		return nil, false, nil
	}

	entryContent, ok := readCacheEntryFile(preparedRequest.cachePath)
	if !ok {
		return nil, false, nil
	}

	entry, ok := decodeCacheEntry(entryContent)
	if !ok {
		return nil, false, nil
	}
	if !cacheEntryMatchesRequest(entry, preparedRequest) {
		return nil, false, nil
	}

	currentManifest, ok := tryBuildManifest(
		preparedRequest.workspaceRoot,
		preparedRequest.relativePath,
		preparedRequest.metadata.AdditionalDependencies,
	)
	if !ok {
		return nil, false, nil
	}
	if !manifestsEqual(entry.Manifest, currentManifest) {
		return nil, false, nil
	}

	return slices.Clone(entry.Payload), true, nil
}

// WriteSymbolTree persists one serialized symbol-tree payload together with the metadata needed for future validation.
func (s *Service) WriteSymbolTree(
	_ context.Context,
	request *stdlsp.WriteSymbolTreeCacheRequest,
) error {
	preparedRequest, err := s.prepareWriteRequest(request)
	if err != nil {
		return err
	}

	manifest, err := buildManifest(
		preparedRequest.workspaceRoot,
		preparedRequest.relativePath,
		preparedRequest.metadata.AdditionalDependencies,
	)
	if err != nil {
		return err
	}

	entryContent, err := json.Marshal(cacheEntry{
		SchemaVersion:           cacheSchemaVersion,
		ArtifactKind:            symbolTreeArtifactKind,
		AdapterID:               preparedRequest.metadata.AdapterID,
		ProfileID:               preparedRequest.metadata.ProfileID,
		AdapterFingerprint:      preparedRequest.metadata.AdapterFingerprint,
		NormalizedWorkspaceRoot: preparedRequest.workspaceRoot,
		RelativePath:            preparedRequest.relativePath,
		Manifest:                manifest,
		Payload:                 slices.Clone(preparedRequest.payload),
	})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(preparedRequest.cachePath), cacheDirPermissions); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}

	return writeAtomically(preparedRequest.cachePath, entryContent)
}

type preparedReadRequest struct {
	workspaceRoot string
	relativePath  string
	cachePath     string
	metadata      stdlsp.SymbolTreeCacheMetadata
}

type preparedWriteRequest struct {
	workspaceRoot string
	relativePath  string
	cachePath     string
	metadata      stdlsp.SymbolTreeCacheMetadata
	payload       []byte
}

func (s *Service) prepareReadRequest(request *stdlsp.ReadSymbolTreeCacheRequest) (*preparedReadRequest, bool) {
	if request == nil {
		return nil, false
	}
	preparedRequest, err := s.prepareBaseRequest(request.WorkspaceRoot, request.RelativePath, &request.Metadata)
	if err != nil {
		return nil, false
	}

	return preparedRequest, true
}

func (s *Service) prepareWriteRequest(request *stdlsp.WriteSymbolTreeCacheRequest) (*preparedWriteRequest, error) {
	if request == nil {
		return nil, errors.New("cache write request is nil")
	}
	preparedBaseRequest, err := s.prepareBaseRequest(request.WorkspaceRoot, request.RelativePath, &request.Metadata)
	if err != nil {
		return nil, err
	}
	if len(request.Payload) == 0 {
		return nil, errors.New("cache payload is empty")
	}

	return &preparedWriteRequest{
		workspaceRoot: preparedBaseRequest.workspaceRoot,
		relativePath:  preparedBaseRequest.relativePath,
		cachePath:     preparedBaseRequest.cachePath,
		metadata:      preparedBaseRequest.metadata,
		payload:       request.Payload,
	}, nil
}

func (s *Service) prepareBaseRequest(
	workspaceRoot string,
	relativePath string,
	metadata *stdlsp.SymbolTreeCacheMetadata,
) (*preparedReadRequest, error) {
	normalizedWorkspaceRoot, err := helpers.ResolveWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	cleanRelativePath, _, err := helpers.ResolveDocumentPath(normalizedWorkspaceRoot, relativePath)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, errors.New("cache metadata is required")
	}
	if strings.TrimSpace(metadata.AdapterID) == "" {
		return nil, errors.New("cache adapter id is required")
	}
	if strings.TrimSpace(metadata.ProfileID) == "" {
		return nil, errors.New("cache profile id is required")
	}
	if strings.TrimSpace(metadata.AdapterFingerprint) == "" {
		return nil, errors.New("cache adapter fingerprint is required")
	}

	return &preparedReadRequest{
		workspaceRoot: normalizedWorkspaceRoot,
		relativePath:  cleanRelativePath,
		cachePath: cacheFilePath(
			s.root,
			normalizedWorkspaceRoot,
			metadata.AdapterID,
			metadata.ProfileID,
			cleanRelativePath,
		),
		metadata: *metadata,
	}, nil
}

func cacheEntryMatchesRequest(entry *cacheEntry, request *preparedReadRequest) bool {
	return entry.SchemaVersion == cacheSchemaVersion &&
		entry.ArtifactKind == symbolTreeArtifactKind &&
		entry.AdapterID == request.metadata.AdapterID &&
		entry.ProfileID == request.metadata.ProfileID &&
		entry.AdapterFingerprint == request.metadata.AdapterFingerprint &&
		entry.NormalizedWorkspaceRoot == request.workspaceRoot &&
		entry.RelativePath == request.relativePath
}

func buildManifest(workspaceRoot, sourceRelativePath string, additionalDependencies []string) ([]manifestEntry, error) {
	uniqueRelativePaths := map[string]struct{}{}
	dependencyPaths := append([]string{sourceRelativePath}, additionalDependencies...)
	manifest := make([]manifestEntry, 0, len(dependencyPaths))

	for _, dependencyPath := range dependencyPaths {
		cleanRelativePath, absolutePath, err := helpers.ResolveDocumentPath(workspaceRoot, dependencyPath)
		if err != nil {
			return nil, err
		}
		if _, seen := uniqueRelativePaths[cleanRelativePath]; seen {
			continue
		}
		contentHash, err := fileContentHash(absolutePath)
		if err != nil {
			return nil, err
		}

		uniqueRelativePaths[cleanRelativePath] = struct{}{}
		manifest = append(manifest, manifestEntry{RelativePath: cleanRelativePath, ContentHash: contentHash})
	}

	sort.Slice(manifest, func(i, j int) bool {
		return manifest[i].RelativePath < manifest[j].RelativePath
	})

	return manifest, nil
}

func manifestsEqual(left, right []manifestEntry) bool {
	if len(left) != len(right) {
		return false
	}

	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}

	return true
}

func readCacheEntryFile(cachePath string) ([]byte, bool) {
	entryContent, err := os.ReadFile(filepath.Clean(cachePath))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false
		}

		return nil, false
	}

	return entryContent, true
}

func decodeCacheEntry(entryContent []byte) (*cacheEntry, bool) {
	var entry cacheEntry
	if json.Unmarshal(entryContent, &entry) != nil {
		return nil, false
	}

	return &entry, true
}

func tryBuildManifest(
	workspaceRoot, sourceRelativePath string,
	additionalDependencies []string,
) ([]manifestEntry, bool) {
	manifest, err := buildManifest(workspaceRoot, sourceRelativePath, additionalDependencies)
	if err != nil {
		return nil, false
	}

	return manifest, true
}

func cacheFilePath(cacheRoot, workspaceRoot, adapterID, profileID, relativePath string) string {
	return filepath.Join(
		cacheRoot,
		workspaceHash(workspaceRoot),
		sharedCacheDirName,
		cacheLayoutVersionDir,
		filepath.Clean(adapterID),
		filepath.Clean(profileID),
		symbolTreeArtifactKind,
		filePathHash(relativePath)+cacheFileExtension,
	)
}

func workspaceHash(normalizedWorkspaceRoot string) string {
	hash := sha256.Sum256([]byte(normalizedWorkspaceRoot))

	return hex.EncodeToString(hash[:])
}

func filePathHash(relativePath string) string {
	hash := sha256.Sum256([]byte(relativePath))

	return hex.EncodeToString(hash[:])
}

func fileContentHash(absolutePath string) (string, error) {
	content, err := os.ReadFile(filepath.Clean(absolutePath))
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(content)

	return hex.EncodeToString(hash[:]), nil
}

func writeAtomically(targetPath string, content []byte) (writeErr error) {
	cleanTargetPath := filepath.Clean(targetPath)
	targetDir := filepath.Clean(filepath.Dir(cleanTargetPath))
	temporaryFile, err := os.CreateTemp(targetDir, filepath.Base(targetPath)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary cache file: %w", err)
	}
	temporaryPath := temporaryFile.Name()
	defer func() {
		if writeErr != nil {
			_ = temporaryFile.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporaryFile.Chmod(cacheFilePermissions); err != nil {
		return fmt.Errorf("chmod temporary cache file: %w", err)
	}
	if _, err = temporaryFile.Write(content); err != nil {
		return fmt.Errorf("write temporary cache file: %w", err)
	}
	if err = temporaryFile.Sync(); err != nil {
		return fmt.Errorf("sync temporary cache file: %w", err)
	}
	if err = temporaryFile.Close(); err != nil {
		return fmt.Errorf("close temporary cache file: %w", err)
	}
	//nolint:gosec // Both paths are derived from the validated cache root and cleaned internal path components.
	if err = os.Rename(filepath.Clean(temporaryPath), cleanTargetPath); err != nil {
		return fmt.Errorf("rename cache file: %w", err)
	}

	targetDirHandle, err := os.Open(targetDir)
	if err != nil {
		return fmt.Errorf("open cache directory for sync: %w", err)
	}
	defer func() {
		writeErr = errors.Join(writeErr, targetDirHandle.Close())
	}()
	if err = targetDirHandle.Sync(); err != nil {
		return fmt.Errorf("sync cache directory: %w", err)
	}

	return nil
}
