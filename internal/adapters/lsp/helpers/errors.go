package helpers

import (
	"context"
	"errors"
)

// sessionError marks an error after which no language server session can serve the request.
type sessionError struct {
	err error
}

// NewSessionError marks err as a failure to get a working language server session, which ends the whole request.
func NewSessionError(err error) error {
	if err == nil {
		return nil
	}

	return &sessionError{err: err}
}

// IsRequestError reports whether err ends the whole request instead of one file: the request context ended,
// or err marks a failure to get a working language server session.
func IsRequestError(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	_, ok := errors.AsType[*sessionError](err)

	return ok
}

// Error returns the text of the underlying error unchanged.
func (e *sessionError) Error() string {
	return e.err.Error()
}

// Unwrap returns the underlying error for errors.Is/errors.As checks.
func (e *sessionError) Unwrap() error {
	return e.err
}
