package lspclangd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/n-r-w/asteria/internal/adapters/lsp/stdlsp"
	"gopkg.in/yaml.v3"
)

const (
	cacheDisabledMissingServerInfo    = "missing_server_info"
	cacheDisabledEmptyServerName      = "empty_server_name"
	cacheDisabledEmptyServerVersion   = "empty_server_version"
	cacheDisabledExternalClangdConfig = "clangd_external_config"
	cacheDisabledUnsupportedClangdCfg = "clangd_unsupported_config"
	cacheDisabledMissingCompDB        = "clangd_missing_compilation_database"
	clangdDependencyCapacity          = 2
)

// clangdConfigFragment keeps the subset of one .clangd YAML document that affects cache validity.
type clangdConfigFragment struct {
	If           clangdConfigIf           `yaml:"If"`
	CompileFlags clangdConfigCompileFlags `yaml:"CompileFlags"`
}

// clangdConfigIf stores the path filters that decide whether one .clangd fragment applies to one file.
type clangdConfigIf struct {
	PathMatch   clangdStringList `yaml:"PathMatch"`
	PathExclude clangdStringList `yaml:"PathExclude"`
}

// clangdConfigCompileFlags keeps the subset of CompileFlags that can change symbol output.
type clangdConfigCompileFlags struct {
	CompilationDatabase string `yaml:"CompilationDatabase"`
}

// clangdStringList accepts the scalar-or-sequence style used by .clangd for path match lists.
type clangdStringList []string

// UnmarshalYAML normalizes .clangd scalar and sequence forms into one Go string slice.
func (l *clangdStringList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.DocumentNode:
		if len(value.Content) != 1 {
			return errors.New("clangd string list document must contain one node")
		}

		return l.UnmarshalYAML(value.Content[0])
	case yaml.ScalarNode:
		*l = []string{value.Value}
		return nil
	case yaml.SequenceNode:
		items := make([]string, 0, len(value.Content))
		for _, item := range value.Content {
			if item.Kind != yaml.ScalarNode {
				return errors.New("clangd string list items must be scalar values")
			}
			items = append(items, item.Value)
		}
		*l = items
		return nil
	case yaml.MappingNode, yaml.AliasNode:
		return errors.New("clangd string list must be a scalar or sequence")
	case 0:
		*l = nil
		return nil
	default:
		return errors.New("unsupported yaml node kind for clangd string list")
	}
}

// buildSymbolTreeCacheMetadata describes the clangd profile inputs required to cache one per-file symbol tree safely.
func (s *Service) buildSymbolTreeCacheMetadata(
	ctx context.Context,
	workspaceRoot string,
	relativePath string,
) (*stdlsp.SymbolTreeCacheMetadata, error) {
	metadata := &stdlsp.SymbolTreeCacheMetadata{
		Enabled:                false,
		DisabledReason:         "",
		AdapterID:              clangdAdapterName,
		ProfileID:              clangdSymbolTreeProfileID,
		AdapterFingerprint:     "",
		AdditionalDependencies: nil,
	}
	additionalDependencies, disabledReason := clangdWorkspaceDependencies(workspaceRoot, relativePath)
	if disabledReason != "" {
		metadata.DisabledReason = disabledReason

		return metadata, nil
	}

	sessionInfo, err := s.rt.SessionInfo(ctx, workspaceRoot)
	if err != nil {
		return nil, err
	}
	if sessionInfo == nil || sessionInfo.InitializeResult == nil || sessionInfo.InitializeResult.ServerInfo == nil {
		metadata.DisabledReason = cacheDisabledMissingServerInfo

		return metadata, nil
	}

	serverName := strings.TrimSpace(sessionInfo.InitializeResult.ServerInfo.Name)
	if serverName == "" {
		metadata.DisabledReason = cacheDisabledEmptyServerName

		return metadata, nil
	}
	serverVersion := strings.TrimSpace(sessionInfo.InitializeResult.ServerInfo.Version)
	if serverVersion == "" {
		metadata.DisabledReason = cacheDisabledEmptyServerVersion

		return metadata, nil
	}

	metadata.Enabled = true
	metadata.AdapterFingerprint = strings.Join([]string{
		clangdAdapterName,
		clangdSymbolTreeProfileID,
		clangdSymbolTreeBehaviorVersion,
		serverName,
		serverVersion,
		languageIDForExtension(filepath.Ext(relativePath)),
	}, "|")
	metadata.AdditionalDependencies = additionalDependencies

	return metadata, nil
}

// clangdWorkspaceDependencies lists workspace-local files that can change clangd symbol output.
func clangdWorkspaceDependencies(workspaceRoot, relativePath string) (dependencies []string, disabledReason string) {
	dependencies = make([]string, 0, clangdDependencyCapacity)
	clangdConfigPath := filepath.Join(workspaceRoot, ".clangd")
	if fileExists(clangdConfigPath) {
		dependencies = append(dependencies, ".clangd")

		compilationDatabaseDependency, compilationDatabaseReason := clangdCompilationDatabaseDependency(
			workspaceRoot,
			relativePath,
			clangdConfigPath,
		)
		if compilationDatabaseReason != "" {
			return nil, compilationDatabaseReason
		}
		if compilationDatabaseDependency != "" {
			dependencies = append(dependencies, compilationDatabaseDependency)
			return deduplicateDependencies(dependencies), ""
		}
	}

	compileCommandsPath := filepath.Join(workspaceRoot, compileCommandsFileName)
	if fileExists(compileCommandsPath) {
		dependencies = append(dependencies, compileCommandsFileName)

		return deduplicateDependencies(dependencies), ""
	}
	compileFlagsPath := filepath.Join(workspaceRoot, compileFlagsFileName)
	if fileExists(compileFlagsPath) {
		dependencies = append(dependencies, compileFlagsFileName)
	}

	return deduplicateDependencies(dependencies), ""
}

// clangdCompilationDatabaseDependency resolves the effective CompilationDatabase for one file so cache policy
// follows the same path-scoped .clangd rules that clangd itself will apply.
func clangdCompilationDatabaseDependency(
	workspaceRoot, relativePath, clangdConfigPath string,
) (dependencyPath, disabledReason string) {
	fragments, ok := loadClangdConfigFragments(clangdConfigPath)
	if !ok {
		return "", cacheDisabledUnsupportedClangdCfg
	}

	effectiveCompilationDatabase := ""
	for _, fragment := range fragments {
		matches, matchesOK := fragment.matches(relativePath)
		if !matchesOK {
			return "", cacheDisabledUnsupportedClangdCfg
		}
		if !matches {
			continue
		}

		candidate := strings.TrimSpace(fragment.CompileFlags.CompilationDatabase)
		if candidate != "" {
			effectiveCompilationDatabase = candidate
		}
	}

	switch effectiveCompilationDatabase {
	case "", "Ancestors":
		return "", ""
	case "None":
		return "", ""
	default:
		return resolveCompilationDatabaseDependency(workspaceRoot, effectiveCompilationDatabase)
	}
}

// loadClangdConfigFragments reads .clangd as a multi-document YAML stream because clangd merges fragments
// in order and cache policy must evaluate the same fragment sequence.
func loadClangdConfigFragments(clangdConfigPath string) ([]clangdConfigFragment, bool) {
	configFile, err := os.Open(filepath.Clean(clangdConfigPath))
	if err != nil {
		return nil, false
	}
	defer func() {
		_ = configFile.Close()
	}()

	decoder := yaml.NewDecoder(configFile)
	fragments := make([]clangdConfigFragment, 0, 1)
	for {
		var fragment clangdConfigFragment
		err = decoder.Decode(&fragment)
		if errors.Is(err, io.EOF) {
			return fragments, true
		}
		if err != nil {
			return nil, false
		}

		fragments = append(fragments, fragment)
	}
}

// matches decides whether one .clangd fragment affects the current file so only applicable settings influence
// the cache dependency set and disable rules.
func (f clangdConfigFragment) matches(relativePath string) (matches, ok bool) {
	normalizedRelativePath := filepath.ToSlash(relativePath)
	if len(f.If.PathMatch) > 0 {
		matchesPath, matchOK := matchesAnyPattern(normalizedRelativePath, f.If.PathMatch)
		if !matchOK || !matchesPath {
			return false, matchOK
		}
	}
	if len(f.If.PathExclude) > 0 {
		excluded, excludeOK := matchesAnyPattern(normalizedRelativePath, f.If.PathExclude)
		if !excludeOK {
			return false, false
		}
		if excluded {
			return false, true
		}
	}

	return true, true
}

// matchesAnyPattern treats invalid regexps as an unsupported config because cache policy must stay conservative
// when it cannot reproduce clangd's fragment selection rules.
func matchesAnyPattern(relativePath string, patterns []string) (matched, ok bool) {
	for _, pattern := range patterns {
		isMatch, err := regexp.MatchString(pattern, relativePath)
		if err != nil {
			return false, false
		}
		if isMatch {
			return true, true
		}
	}

	return false, true
}

// resolveCompilationDatabaseDependency converts one configured CompilationDatabase into a workspace-relative
// manifest dependency and rejects paths that would make cache validity depend on unmanaged external files.
func resolveCompilationDatabaseDependency(
	workspaceRoot, configuredPath string,
) (dependencyPath, disabledReason string) {
	trimmedPath := strings.TrimSpace(configuredPath)
	if trimmedPath == "" {
		return "", ""
	}

	resolvedPath := trimmedPath
	if !filepath.IsAbs(resolvedPath) {
		resolvedPath = filepath.Join(workspaceRoot, resolvedPath)
	}
	cleanResolvedPath := filepath.Clean(resolvedPath)
	workspacePrefix := filepath.Clean(workspaceRoot) + string(filepath.Separator)
	if cleanResolvedPath != filepath.Clean(workspaceRoot) && !strings.HasPrefix(cleanResolvedPath, workspacePrefix) {
		return "", cacheDisabledExternalClangdConfig
	}

	compilationDatabasePath := cleanResolvedPath
	if filepath.Base(cleanResolvedPath) != compileCommandsFileName {
		compilationDatabasePath = filepath.Join(cleanResolvedPath, compileCommandsFileName)
	}
	if !fileExists(compilationDatabasePath) {
		return "", cacheDisabledMissingCompDB
	}

	relativeDependencyPath, err := filepath.Rel(workspaceRoot, compilationDatabasePath)
	if err != nil {
		return "", cacheDisabledUnsupportedClangdCfg
	}

	// Cache manifests use workspace-relative slash paths so the same logical dependency hashes the same on every OS.
	return filepath.ToSlash(filepath.Clean(relativeDependencyPath)), ""
}

// deduplicateDependencies keeps the manifest stable so the same logical dependency set does not create
// cache churn from repeated or differently formatted path entries.
func deduplicateDependencies(dependencies []string) []string {
	uniqueDependencies := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		cleanDependency := filepath.Clean(dependency)
		if slices.Contains(uniqueDependencies, cleanDependency) {
			continue
		}

		uniqueDependencies = append(uniqueDependencies, cleanDependency)
	}

	return uniqueDependencies
}
