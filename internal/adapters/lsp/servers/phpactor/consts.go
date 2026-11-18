// Package lspphpactor implements the phpactor standard-LSP adapter.
package lspphpactor

import (
	"io/fs"
	"time"
)

// extensions lists the file extensions supported by phpactor for symbolic search.
//
//nolint:gochecknoglobals // ok for adapter-level constants
var extensions = []string{phpExtension}

const (
	phpExtension                 = ".php"
	phpactorServerName           = "phpactor"
	phpLanguageID                = "php"
	phpactorIndexerDirName       = "index"
	phpactorStateDirPermissions  = fs.FileMode(0o750)
	phpactorIndexerPathKey       = "indexer.index_path"
	phpactorEnabledWatchersKey   = "indexer.enabled_watchers"
	phpactorLSPWatcher           = "lsp"
	phpactorPHPStanEnabledKey    = "language_server_phpstan.enabled"
	phpactorPsalmEnabledKey      = "language_server_psalm.enabled"
	phpactorPHPCSFixerEnabledKey = "language_server_php_cs_fixer.enabled"
	// phpactorAutoConfigKey controls whether phpactor writes settings for the tools that it detects in composer.lock
	// into .phpactor.json of the project.
	phpactorAutoConfigKey = "language_server_configuration.auto_config"
	// phpactorIndexingStartTimeout is the longest time after the start of a session that reference searches wait
	// while no indexing progress has begun; an indexing progress that begins later makes searches wait for its end
	// again.
	phpactorIndexingStartTimeout = 30 * time.Second
	// progressKindBegin is the kind of the work-done progress notification that starts a progress.
	progressKindBegin = "begin"
	// progressKindEnd is the kind of the work-done progress notification that ends a progress.
	progressKindEnd = "end"
)

const (
	phpactorDiagnosticsOnOpenKey   = "language_server.diagnostics_on_open"
	phpactorDiagnosticsOnUpdateKey = "language_server.diagnostics_on_update"
	phpactorDiagnosticsOnSaveKey   = "language_server.diagnostics_on_save"
)
