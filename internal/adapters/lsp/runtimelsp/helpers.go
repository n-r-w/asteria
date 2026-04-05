package runtimelsp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// newShutdownContext gives runtime cleanup one bounded budget that is independent
// from the caller's remaining test time.
func newShutdownContext(ctx context.Context, shutdownTimeout time.Duration) (context.Context, context.CancelFunc) {
	if shutdownTimeout <= 0 {
		shutdownTimeout = defaultShutdownTimeout
	}

	if ctx == nil {
		return context.WithTimeout(context.Background(), shutdownTimeout)
	}

	return context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
}

// killProcess force-stops the language server only when graceful shutdown missed the deadline.
func killProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}

	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}

	return nil
}

// normalizeWaitError hides expected termination signals emitted during explicit runtime shutdown.
func normalizeWaitError(err error) error {
	if err == nil {
		return nil
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return err
	}

	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return err
	}

	if status.Signal() == syscall.SIGPIPE || status.Signal() == syscall.SIGKILL {
		return nil
	}

	return err
}

// normalizeWaitErrorOnShutdown hides the final process exit status once runtime shutdown has already started.
func normalizeWaitErrorOnShutdown(err error) error {
	if normalizedErr := normalizeWaitError(err); normalizedErr == nil {
		return nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}

	return err
}

// normalizeConnCloseError hides expected transport-close noise after the language server has already exited.
func normalizeConnCloseError(err error) error {
	if err == nil {
		return nil
	}

	if onlyExpectedCloseErrors(err) {
		return nil
	}

	return err
}

func onlyExpectedCloseErrors(err error) bool {
	if err == nil {
		return true
	}

	type multiUnwrapper interface {
		Unwrap() []error
	}
	type singleUnwrapper interface {
		Unwrap() error
	}

	if multiErr, ok := err.(multiUnwrapper); ok {
		children := multiErr.Unwrap()
		if len(children) == 0 {
			return errors.Is(err, os.ErrClosed)
		}

		for _, child := range children {
			if !onlyExpectedCloseErrors(child) {
				return false
			}
		}

		return true
	}

	if wrappedErr, ok := err.(singleUnwrapper); ok {
		child := wrappedErr.Unwrap()
		if child == nil {
			return errors.Is(err, os.ErrClosed)
		}

		return onlyExpectedCloseErrors(child)
	}

	return errors.Is(err, os.ErrClosed)
}
