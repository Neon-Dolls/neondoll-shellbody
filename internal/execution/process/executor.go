package process

import (
	"bytes"
	"context"
	"os/exec"
	"syscall"
	"time"
)

// Executor handles process execution for the shell capability
type Executor struct{}

// NewExecutor creates a new process executor.
func NewExecutor() *Executor {
	return &Executor{}
}

// Execute runs a command with the given context and returns the result.
func (e *Executor) Execute(ctx context.Context, command string, args []string, env []string, dir string, stdin string, timeout time.Duration) (int, string, string, error) {
	// Create a context that respects both the parent ctx and the timeout.
	var cancel context.CancelFunc
	var execCtx context.Context
	if timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	} else {
		execCtx = ctx
	}

	// Create the command with context
	cmd := exec.CommandContext(execCtx, command, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != "" {
		cmd.Stdin = bytes.NewBufferString(stdin)
	}
	// Start the process
	err := cmd.Start()
	if err != nil {
		return -1, "", "", err
	}
	// Wait for the process to finish or context cancellation
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case <-execCtx.Done():
		// Context cancelled (either parent ctx or timeout), try to kill the process
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done // Wait for the process to be reaped
		return -1, stdout.String(), stderr.String(), execCtx.Err()
	case err = <-done:
		if err != nil {
			// Check if the error is due to signal
			if exitErr, ok := err.(*exec.ExitError); ok {
				if waitStatus, ok := exitErr.Sys().(syscall.WaitStatus); ok {
					return waitStatus.ExitStatus(), stdout.String(), stderr.String(), nil
				}
			}
			return -1, stdout.String(), stderr.String(), err
		}
		// Success
		if cmd.ProcessState != nil {
			return cmd.ProcessState.ExitCode(), stdout.String(), stderr.String(), nil
		}
		return 0, stdout.String(), stderr.String(), nil
	}
}
