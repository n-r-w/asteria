// Package lspclangd implements the clangd standard-LSP adapter.
package lspclangd

// extensions lists the file extensions supported by clangd in the first Asteria version.
//
//nolint:gochecknoglobals // ok for adapter-level constants
var extensions = []string{
	".c",
	".C",
	".cc",
	".CC",
	".cp",
	".cpp",
	".CPP",
	".cxx",
	".CXX",
	".c++",
	".C++",
	".h",
	".H",
	".hh",
	".hpp",
	".hxx",
	".ccm",
	".cppm",
	".cxxm",
	".c++m",
}

const (
	clangdServerName                 = "clangd"
	clangdLanguageIDC                = "c"
	clangdLanguageIDCPP              = "cpp"
	clangdCompilationDatabasePathKey = "compilationDatabasePath"
	compileCommandsFileName          = "compile_commands.json"
	compileFlagsFileName             = "compile_flags.txt"
	clangdAdapterName                = "clangd"
	clangdSymbolTreeProfileID        = "std"
	clangdSymbolTreeBehaviorVersion  = "clangd-symbol-tree-v1"
	clangdCacheDirPermissions        = 0o755
	clangdManagedFilePermissions     = 0o600
)

// languageIDForExtension maps one file extension to the clangd language ID used when files are opened through LSP.
func languageIDForExtension(ext string) string {
	switch ext {
	case ".c":
		return clangdLanguageIDC
	default:
		return clangdLanguageIDCPP
	}
}
