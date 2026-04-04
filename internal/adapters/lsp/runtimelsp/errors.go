package runtimelsp

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

var errRuntimeClosing = errors.New("runtime is closing")

// wrapShutdownError keeps shutdown cleanup readable when some steps legitimately return nil.
func wrapShutdownError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%s: %w", operation, err)
}

// wrapSessionError preserves stderr because startup failures are otherwise opaque.
func wrapSessionError(operation string, err error, stderr *bytes.Buffer) error {
	stderrOutput := strings.TrimSpace(stderr.String())
	if stderrOutput == "" {
		return fmt.Errorf("%s: %w", operation, err)
	}

	return fmt.Errorf("%s: %w: %s", operation, err, stderrOutput)
}
