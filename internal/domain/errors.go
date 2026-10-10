package domain

// ToolError describes a failed tool request with context message and optional cause.
type ToolError struct {
	message string
	cause   error
}

// NewToolError creates one error with context message and optional cause.
func NewToolError(message string, cause error) *ToolError {
	return &ToolError{message: message, cause: cause}
}

// Error returns the context message followed by the cause.
func (e *ToolError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause == nil {
		return e.message
	}

	return e.message + ": " + e.cause.Error()
}

// Unwrap returns the underlying cause for errors.Is/errors.As checks.
func (e *ToolError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.cause
}

// Cause returns the underlying cause.
func (e *ToolError) Cause() error {
	if e == nil {
		return nil
	}

	return e.cause
}
