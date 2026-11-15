package runtimelsp

import "time"

const (
	// defaultShutdownTimeout keeps process cleanup bounded when the caller does not provide a deadline.
	defaultShutdownTimeout = 5 * time.Second
)

const defaultSessionIdleTimeout = 10 * time.Minute

// processWaitDelay bounds how long Wait reads the output of an exited command that a descendant still holds open.
const processWaitDelay = 2 * time.Second
