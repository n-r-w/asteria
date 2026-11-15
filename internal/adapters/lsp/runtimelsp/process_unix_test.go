//go:build unix

package runtimelsp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

const (
	// processTreeHelperEnv marks the re-executed test binary that plays the asteria process.
	processTreeHelperEnv = "ASTERIA_TEST_PROCESS_TREE_HELPER"
	// processTreeTimeout bounds each wait for processes of a tree to appear or to end.
	processTreeTimeout = 10 * time.Second
	// processTreeWaitMargin is the time that Wait may take beyond processWaitDelay on a loaded machine.
	processTreeWaitMargin = 3 * time.Second
	// processTreePollInterval sets how often the test checks that a process group is gone.
	processTreePollInterval = 20 * time.Millisecond
	// processTreeHelperLifetime keeps the helper alive far longer than any test waits for it.
	processTreeHelperLifetime = 10 * time.Minute
	// processTreeServerScript starts a child, reports "own PID, child PID" on stdout, and waits for the child.
	processTreeServerScript = `sleep 600 & echo "$$ $!"; wait`
	// processTreeExitingScript starts a child, reports "own PID, child PID" on stdout, and exits at once.
	processTreeExitingScript = `sleep 600 & echo "$$ $!"`
	// processTreeOutputScript writes "out" to stdout and "err" to stderr, starts a child, reports "own PID, child PID"
	// into the file from its first argument, and waits for the child.
	processTreeOutputScript = `echo out; echo err >&2; sleep 600 & echo "$$ $!" > "$1"; wait`
)

// TestCommandHelperProcess is not a real test: the owner test re-executes the test binary with this test selected
// so that a separate process starts a command tree and can then be killed with SIGKILL.
func TestCommandHelperProcess(t *testing.T) {
	t.Parallel()

	if os.Getenv(processTreeHelperEnv) != "1" {
		t.Skip("runs only as the helper process of commandTreeSuite.TestTreeEndsWhenOwnerIsKilled")
	}

	cmd := NewCommand(t.Context(), "/bin/sh", "-c", processTreeServerScript)
	cmd.Stdout = os.Stdout
	require.NoError(t, cmd.Start())

	time.Sleep(processTreeHelperLifetime)
}

// commandTreeSuite checks that each way to run a Command leaves no process of its tree behind on Unix.
type commandTreeSuite struct {
	suite.Suite
}

// TestCommandTreeSuite runs the checks that no process of a Command tree outlives its owner on Unix.
func TestCommandTreeSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(commandTreeSuite))
}

// TestTreeEndsWhenOwnerIsKilled proves that a command and its child end when the process that started the command
// dies by SIGKILL and runs no cleanup code.
func (s *commandTreeSuite) TestTreeEndsWhenOwnerIsKilled() {
	t := s.T()

	reader, writer, err := os.Pipe()
	s.Require().NoError(err)
	t.Cleanup(func() { _ = reader.Close() })

	owner := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestCommandHelperProcess$")
	owner.Env = append(os.Environ(), processTreeHelperEnv+"=1")
	owner.Stdout = writer
	s.Require().NoError(owner.Start())
	s.Require().NoError(writer.Close())

	output, pids := readProcessTreePIDs(t, reader)

	s.Require().NoError(owner.Process.Kill())
	_ = owner.Wait()

	requireProcessTreeGone(t, reader, output, pids)
}

// TestKillEndsProcessTree proves that a forced stop ends the command and the child that it started.
func (s *commandTreeSuite) TestKillEndsProcessTree() {
	t := s.T()

	cmd := NewCommand(t.Context(), "/bin/sh", "-c", processTreeServerScript)
	reader, output, pids := startProcessTree(t, cmd)

	s.Require().NoError(cmd.Kill())
	s.Require().Error(cmd.Wait())

	requireProcessTreeGone(t, reader, output, pids)
}

// TestWaitEndsOrphanedChildren proves that the children a command leaves behind end once the command has exited on
// its own and Wait has returned.
func (s *commandTreeSuite) TestWaitEndsOrphanedChildren() {
	t := s.T()

	cmd := NewCommand(t.Context(), "/bin/sh", "-c", processTreeExitingScript)
	reader, output, pids := startProcessTree(t, cmd)

	s.Require().NoError(cmd.Wait())

	requireProcessTreeGone(t, reader, output, pids)
}

// TestContextCancelEndsProcessTree proves that canceling the command context ends the command and its child.
func (s *commandTreeSuite) TestContextCancelEndsProcessTree() {
	t := s.T()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cmd := NewCommand(ctx, "/bin/sh", "-c", processTreeServerScript)
	reader, output, pids := startProcessTree(t, cmd)

	cancel()
	s.Require().Error(cmd.Wait())

	requireProcessTreeGone(t, reader, output, pids)
}

// TestRunReturnsWhenCommandExits proves that the guard does not hold the output pipes of the command, so Run returns
// as soon as the command exits while asteria captures its output.
func (s *commandTreeSuite) TestRunReturnsWhenCommandExits() {
	cmd := NewCommand(s.T().Context(), "/bin/sh", "-c", "echo out; echo err >&2")
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runResult := make(chan error, 1)
	go func() { runResult <- cmd.Run() }()

	select {
	case err := <-runResult:
		s.Require().NoError(err)
	case <-time.After(processTreeTimeout):
		s.Require().NoError(cmd.Kill())
		s.Require().Fail("Run did not return after the command exited")
	}
	s.Require().Equal("out\n", stdout.String())
	s.Require().Equal("err\n", stderr.String())
}

// TestWaitReturnsWhenChildHoldsOutput proves that Wait returns soon after a command exits even though its child
// still holds the captured stdout and stderr, and that the child then ends.
func (s *commandTreeSuite) TestWaitReturnsWhenChildHoldsOutput() {
	t := s.T()

	cmd := NewCommand(t.Context(), "/bin/sh", "-c", processTreeExitingScript)
	stdout := bytes.Buffer{}
	stderr := bytes.Buffer{}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	s.Require().NoError(cmd.Start())

	waitResult := make(chan error, 1)
	go func() { waitResult <- cmd.Wait() }()

	select {
	case err := <-waitResult:
		s.Require().ErrorIs(err, exec.ErrWaitDelay)
	case <-time.After(processWaitDelay + processTreeWaitMargin):
		s.Require().NoError(cmd.Kill())
		s.Require().Error(<-waitResult)
		s.Require().Fail("Wait did not return after the command exited while its child held the output")
	}

	pids := parseProcessTreePIDs(t, stdout.String())
	requireProcessesGone(t, pids)
}

// TestOutputEndsProcessTreeOnKill proves that Output and CombinedOutput run the command inside its process tree, so a
// forced stop ends the command and its child, and that both return the output of the command.
func (s *commandTreeSuite) TestOutputEndsProcessTreeOnKill() {
	cases := []struct {
		name   string
		run    func(*Command) ([]byte, error)
		output string
	}{
		{name: "Output", run: (*Command).Output, output: "out\n"},
		{name: "CombinedOutput", run: (*Command).CombinedOutput, output: "out\nerr\n"},
	}

	for _, testCase := range cases {
		s.Run(testCase.name, func() {
			t := s.T()

			pidFile := filepath.Join(t.TempDir(), "pids")
			cmd := NewCommand(t.Context(), "/bin/sh", "-c", processTreeOutputScript, "sh", pidFile)

			type outputResult struct {
				output []byte
				err    error
			}
			results := make(chan outputResult, 1)
			go func() {
				output, err := testCase.run(cmd)
				results <- outputResult{output: output, err: err}
			}()

			var report string
			s.Require().Eventually(func() bool {
				content, err := os.ReadFile(pidFile)
				report = string(content)
				return err == nil && strings.HasSuffix(report, "\n")
			}, processTreeTimeout, processTreePollInterval, "the command did not report its PIDs")
			pids := parseProcessTreePIDs(t, report)

			s.Require().NoError(cmd.Kill())

			select {
			case result := <-results:
				s.Require().Error(result.err)
				s.Require().Equal(testCase.output, string(result.output))
			case <-time.After(processTreeTimeout):
				s.Require().Fail(testCase.name + " did not return after a forced stop")
			}

			requireProcessesGone(t, pids)
		})
	}
}

// processTreePIDs holds the PIDs that a test command reports about itself and its child.
type processTreePIDs struct {
	command int
	child   int
	// gone disarms the cleanup kill once the test has seen both processes end, because their PIDs may be reused.
	gone bool
}

// startProcessTree starts cmd with stdout on a pipe that only the command tree holds and returns the reported PIDs.
func startProcessTree(t *testing.T, cmd *Command) (*os.File, *bufio.Reader, *processTreePIDs) {
	t.Helper()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = reader.Close() })

	cmd.Stdout = writer
	require.NoError(t, cmd.Start())
	require.NoError(t, writer.Close())

	output, pids := readProcessTreePIDs(t, reader)

	return reader, output, pids
}

// readProcessTreePIDs reads the PID line of a test command from its stdout pipe.
func readProcessTreePIDs(t *testing.T, reader *os.File) (*bufio.Reader, *processTreePIDs) {
	t.Helper()

	require.NoError(t, reader.SetReadDeadline(time.Now().Add(processTreeTimeout)))
	output := bufio.NewReader(reader)
	line, err := output.ReadString('\n')
	require.NoError(t, err)

	return output, parseProcessTreePIDs(t, line)
}

// parseProcessTreePIDs parses the "own PID, child PID" report of a test command and registers cleanup that kills
// both processes unless the test has seen them end.
func parseProcessTreePIDs(t *testing.T, report string) *processTreePIDs {
	t.Helper()

	fields := strings.Fields(report)
	require.Len(t, fields, 2)
	commandPID, err := strconv.Atoi(fields[0])
	require.NoError(t, err)
	childPID, err := strconv.Atoi(fields[1])
	require.NoError(t, err)

	pids := &processTreePIDs{
		command: commandPID,
		child:   childPID,
		gone:    false,
	}
	t.Cleanup(func() {
		if pids.gone {
			return
		}
		_ = syscall.Kill(pids.command, syscall.SIGKILL)
		_ = syscall.Kill(pids.child, syscall.SIGKILL)
	})

	return pids
}

// requireProcessTreeGone waits until no process of the tree holds the command stdout and the process group of
// the command and the child are gone.
func requireProcessTreeGone(t *testing.T, reader *os.File, output *bufio.Reader, pids *processTreePIDs) {
	t.Helper()

	require.NoError(t, reader.SetReadDeadline(time.Now().Add(processTreeTimeout)))
	_, err := io.Copy(io.Discard, output)
	require.NoError(t, err, "a process of the command tree still holds the command stdout")

	requireProcessesGone(t, pids)
}

// requireProcessesGone waits until the process group of the command and the child are gone, then disarms the
// cleanup kill of their PIDs.
func requireProcessesGone(t *testing.T, pids *processTreePIDs) {
	t.Helper()

	require.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(-pids.command, 0), syscall.ESRCH) &&
			errors.Is(syscall.Kill(pids.child, 0), syscall.ESRCH)
	}, processTreeTimeout, processTreePollInterval, "a process of the command tree is still running")
	pids.gone = true
}
