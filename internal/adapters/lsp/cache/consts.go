// Package cache stores validated on-disk artifacts for shared LSP symbol workflows.
package cache

const (
	sharedCacheDirName     = "shared"
	cacheLayoutVersionDir  = "v1"
	symbolTreeArtifactKind = "symbol_tree"
	cacheSchemaVersion     = 1
	cacheFileExtension     = ".json"
	cacheDirPermissions    = 0o755
	cacheFilePermissions   = 0o600
)
