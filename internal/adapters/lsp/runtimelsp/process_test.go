package runtimelsp

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/suite"
)

// commandSuite checks the Command behavior that does not depend on the operating system.
type commandSuite struct {
	suite.Suite
}

// TestCommandSuite runs the checks of Command that do not depend on the operating system.
func TestCommandSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(commandSuite))
}

// TestStartReportsMissingExecutable proves that a command that is not installed fails at Start with the standard
// lookup error instead of failing later inside a started process.
func (s *commandSuite) TestStartReportsMissingExecutable() {
	cmd := NewCommand(s.T().Context(), "asteria-test-missing-executable")

	s.Require().ErrorIs(cmd.Start(), exec.ErrNotFound)
}
