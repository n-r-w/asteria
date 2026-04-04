// Package lspphpactor implements the phpactor standard-LSP adapter.
package lspphpactor

import "io/fs"

// extensions lists the file extensions supported by phpactor for symbolic search.
//
//nolint:gochecknoglobals // ok for adapter-level constants
var extensions = []string{".php"}

const (
	phpactorServerName           = "phpactor"
	phpLanguageID                = "php"
	phpactorIndexerDirName       = "index"
	phpactorStateDirPermissions  = fs.FileMode(0o750)
	phpactorEnabledWatchersKey   = "indexer.enabled_watchers"
	phpactorIndexerPathKey       = "indexer.index_path"
	phpactorPHPStanEnabledKey    = "language_server_phpstan.enabled"
	phpactorPsalmEnabledKey      = "language_server_psalm.enabled"
	phpactorPHPCSFixerEnabledKey = "language_server_php_cs_fixer.enabled"
	phpactorLSPWatcherName       = "lsp"
)
