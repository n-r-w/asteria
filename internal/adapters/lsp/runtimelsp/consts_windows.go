//go:build windows

package runtimelsp

// processTreeKillExitCode is the exit code of processes that a forced stop terminates, the same as os.Process.Kill.
const processTreeKillExitCode = 1
