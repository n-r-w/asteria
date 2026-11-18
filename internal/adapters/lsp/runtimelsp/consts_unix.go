//go:build unix

package runtimelsp

const (
	// processGuardShell starts the guard that ties a command started on Unix to asteria.
	processGuardShell = "/bin/sh"
	// processGuardName is the $0 of the guard script, which the shell shows in its own error messages.
	processGuardName = "asteria-process-guard"
	// processGuardScript starts a watcher that kills the process group once fd 3, the lifeline, closes, then
	// replaces the shell with the command from its positional parameters, so the command keeps the PID and the group.
	processGuardScript = `( read -r _ <&3; kill -s KILL 0 ) </dev/null >/dev/null 2>&1 & exec 3<&- "$@"`
)
