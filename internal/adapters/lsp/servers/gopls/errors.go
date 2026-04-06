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

// sanitizeGoplsPublicError keeps Go-specific public error classification inside the gopls adapter.
func sanitizeGoplsPublicError(argName, path string, err error) error {
	if err == nil {
		return nil
	}

	if safeErr, ok := errors.AsType[*domain.SafeError](err); ok {
		return safeErr
	}
	if !isGoplsNoPackageMetadataError(err) {
		return err
	}

	publicArgName := strings.TrimSpace(argName)
	if publicArgName == "" {
		publicArgName = "file_path"
	}

	return domain.NewSafeError(
		fmt.Sprintf(goplsExcludedBuildFileMessageFormat, publicArgName, domain.NormalizePublicPath(path)),
		err,
	)
}

// isGoplsNoPackageMetadataError matches the stable gopls text that marks files excluded from the active build graph.
func isGoplsNoPackageMetadataError(err error) bool {
	return strings.Contains(err.Error(), goplsNoPackageMetadataFragment)
}
