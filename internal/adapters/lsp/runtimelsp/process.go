package runtimelsp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
)

// Command runs an external command that ends together with asteria, along with each descendant that stays in its
// Unix process group or Windows job.
type Command struct {
	*exec.Cmd

	mu     sync.Mutex
	waited bool
	tree   processTree
}

// NewCommand prepares a command like exec.CommandContext, where canceling ctx kills the whole process tree, Wait
// stops reading output that descendants hold open soon after the command exits, and Start replaces Path, Args,
// ExtraFiles, and SysProcAttr of the embedded exec.Cmd.
func NewCommand(ctx context.Context, name string, args ...string) *Command {
	command := &Command{
		//nolint:gosec // Adapters pass commands built in code, and arguments reach the guard only as positional parameters.
		Cmd:    exec.CommandContext(ctx, name, args...),
		mu:     sync.Mutex{},
		waited: false,
		tree:   processTree{},
	}
	command.Cancel = command.Kill
	command.WaitDelay = processWaitDelay

	return command
}

// Start starts the command bound to the lifetime of asteria.
func (c *Command) Start() error {
	c.mu.Lock()
	err := c.tree.start(c.Cmd)
	c.mu.Unlock()

	// A process that started but could not be bound to asteria is already killed; Wait reaps it without the lock,
	// because exec.Cmd.Wait waits for a Cancel call that needs the lock.
	if err != nil && c.Process != nil {
		_ = c.Wait()
	}

	return err
}

// Wait waits for the command to exit and then ends the descendants that outlived it; exec.ErrWaitDelay means that the
// command succeeded while a descendant still held its output.
func (c *Command) Wait() error {
	waitErr := c.Cmd.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()

	c.waited = true
	if releaseErr := c.tree.release(); releaseErr != nil {
		return errors.Join(waitErr, fmt.Errorf("release process tree: %w", releaseErr))
	}

	return waitErr
}

// Run starts the command and waits for it.
func (c *Command) Run() error {
	if err := c.Start(); err != nil {
		return err
	}

	return c.Wait()
}

// Output runs the command like exec.Cmd.Output and returns its standard output.
func (c *Command) Output() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errStdoutAlreadySet
	}

	stdout := bytes.Buffer{}
	c.Stdout = &stdout

	captureStderr := c.Stderr == nil
	stderr := bytes.Buffer{}
	if captureStderr {
		c.Stderr = &stderr
	}

	err := c.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok && captureStderr {
		exitErr.Stderr = stderr.Bytes()
	}

	return stdout.Bytes(), err
}

// CombinedOutput runs the command like exec.Cmd.CombinedOutput and returns its standard output and standard error.
func (c *Command) CombinedOutput() ([]byte, error) {
	if c.Stdout != nil {
		return nil, errStdoutAlreadySet
	}
	if c.Stderr != nil {
		return nil, errStderrAlreadySet
	}

	output := bytes.Buffer{}
	c.Stdout = &output
	c.Stderr = &output
	err := c.Run()

	return output.Bytes(), err
}

// Kill ends the command and all its descendants at once; it returns os.ErrProcessDone after Wait has returned.
func (c *Command) Kill() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.Process == nil {
		return nil
	}
	// After Wait the tree is released, so the OS may reuse its identifiers for unrelated processes.
	if c.waited {
		return os.ErrProcessDone
	}

	return c.tree.kill(c.Process)
}
