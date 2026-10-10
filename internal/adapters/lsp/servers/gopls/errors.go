package lspgopls

import (
	"errors"
	"fmt"
	"strings"

	"github.com/n-r-w/asteria/internal/domain"
)

const (
	goplsNoPackageMetadataFragment      = "no package metadata for file"
	goplsExcludedBuildFileMessageFormat = "%s %q is excluded from the active Go build configuration; " +
		"include its build tags in the active build flags"
)

// explainGoplsBuildTagError adds build flag advice to gopls errors for files outside the active build.
func explainGoplsBuildTagError(argName, path string, err error) error {
	if err == nil {
		return nil
	}

	if toolErr, ok := errors.AsType[*domain.ToolError](err); ok {
		return toolErr
	}
	if !isGoplsNoPackageMetadataError(err) {
		return err
	}

	publicArgName := strings.TrimSpace(argName)
	if publicArgName == "" {
		publicArgName = "file_path"
	}

	return domain.NewToolError(
		fmt.Sprintf(goplsExcludedBuildFileMessageFormat, publicArgName, domain.NormalizeMessagePath(path)),
		err,
	)
}

// isGoplsNoPackageMetadataError matches the stable gopls text that marks files excluded from the active build graph.
func isGoplsNoPackageMetadataError(err error) bool {
	return strings.Contains(err.Error(), goplsNoPackageMetadataFragment)
}
