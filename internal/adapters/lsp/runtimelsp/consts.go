package runtimelsp

import "time"

const (
	// defaultShutdownTimeout keeps process cleanup bounded when the caller does not provide a deadline.
	defaultShutdownTimeout = 5 * time.Second
)
