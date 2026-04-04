package cache

// manifestEntry keeps one workspace-relative dependency file and its content hash.
type manifestEntry struct {
	RelativePath string `json:"relative_path"`
	ContentHash  string `json:"content_hash"`
}

// cacheEntry keeps one serialized symbol-tree artifact together with the metadata required to validate it.
type cacheEntry struct {
	SchemaVersion           int             `json:"schema_version"`
	ArtifactKind            string          `json:"artifact_kind"`
	AdapterID               string          `json:"adapter_id"`
	ProfileID               string          `json:"profile_id"`
	AdapterFingerprint      string          `json:"adapter_fingerprint"`
	NormalizedWorkspaceRoot string          `json:"normalized_workspace_root"`
	RelativePath            string          `json:"relative_path"`
	Manifest                []manifestEntry `json:"manifest"`
	Payload                 []byte          `json:"payload"`
}
