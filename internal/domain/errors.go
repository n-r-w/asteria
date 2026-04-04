package domain

// SafeError keeps one public-safe message for MCP clients while preserving the original cause for logs.
type SafeError struct {
	message string
	cause   error
}

// NewSafeError creates one error whose Error() text is safe to expose to MCP clients.
func NewSafeError(message string, cause error) *SafeError {
	return &SafeError{message: message, cause: cause}
}

// Error returns the public-safe error message.
func (e *SafeError) Error() string {
	if e == nil {
		return ""
	}

	return e.message
}

// Unwrap returns the internal cause for logs and errors.Is/errors.As checks.
func (e *SafeError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.cause
}

// Cause returns the internal cause without changing the public error string.
func (e *SafeError) Cause() error {
	if e == nil {
		return nil
	}

	return e.cause
}
