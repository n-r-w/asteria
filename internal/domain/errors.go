package domain

// SafeError keeps one client-facing context message together with its optional underlying cause.
type SafeError struct {
	message string
	cause   error
}

// NewSafeError creates one error with a client-facing context message and an optional diagnostic cause.
func NewSafeError(message string, cause error) *SafeError {
	return &SafeError{message: message, cause: cause}
}

// Error returns the client-facing context message.
func (e *SafeError) Error() string {
	if e == nil {
		return ""
	}

	return e.message
}

// Unwrap returns the underlying cause for MCP responses, logs, and errors.Is/errors.As checks.
func (e *SafeError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.cause
}

// Cause returns the underlying cause without changing the context returned by Error.
func (e *SafeError) Cause() error {
	if e == nil {
		return nil
	}

	return e.cause
}
