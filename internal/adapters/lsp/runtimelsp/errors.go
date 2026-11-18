package runtimelsp

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

var errRuntimeClosing = errors.New("runtime is closing")

// errSessionClosed reports that the session that a request acquired was closed, so the request cannot continue on it.
var errSessionClosed = errors.New("LSP session closed")

var (
	// errStdoutAlreadySet repeats the exec.Cmd error for a caller that set Stdout before an output method.
	errStdoutAlreadySet = errors.New("exec: Stdout already set")
	// errStderrAlreadySet repeats the exec.Cmd error for a caller that set Stderr before CombinedOutput.
	errStderrAlreadySet = errors.New("exec: Stderr already set")
)

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
