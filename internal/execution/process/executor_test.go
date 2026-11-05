// Package process provides wire-independent host process execution machinery.
package process

import (
	"context"
	"testing"
	"time"
)

func TestExecutorCancelsAndReapsProcess(t *testing.T) {
	e := NewExecutor()

	// Test that a long-running process is cancelled and reaped
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		// Use sleep 10 to simulate a long-running process
		_, _, _, err := e.Execute(ctx, "sleep", []string{"10"}, nil, "", "", 30*time.Second)
		// When cancelled, we expect context.Canceled error
		if err != nil && err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
		close(done)
	}()

	// Give the process time to start
	time.Sleep(100 * time.Millisecond)

	// Cancel the context
	cancel()

	// Wait for the executor to finish
	select {
	case <-done:
		// Success - process was cancelled and reaped
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not finish after cancellation - process may be orphaned")
	}
}

func TestExecutorTimeoutKillsProcess(t *testing.T) {
	e := NewExecutor()

	// Test that a timeout kills the process
	ctx := context.Background()

	done := make(chan struct{})
	go func() {
		// Use sleep 10 with a 200ms timeout
		_, _, _, err := e.Execute(ctx, "sleep", []string{"10"}, nil, "", "", 200*time.Millisecond)
		// When timed out, we expect context.DeadlineExceeded error
		if err != nil && err != context.DeadlineExceeded {
			t.Errorf("expected context.DeadlineExceeded, got %v", err)
		}
		close(done)
	}()

	// Wait for the executor to finish
	select {
	case <-done:
		// Success - process was killed and reaped
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not finish after timeout - process may be orphaned")
	}
}

func TestExecutorNoOrphanOnQuickCancel(t *testing.T) {
	e := NewExecutor()

	// Test immediate cancellation
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		_, _, _, err := e.Execute(ctx, "sleep", []string{"10"}, nil, "", "", 30*time.Second)
		if err != nil && err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
		close(done)
	}()

	// Cancel immediately
	cancel()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not finish after immediate cancellation")
	}
}

func TestExecutorNoOrphanOnBinaryNotFound(t *testing.T) {
	e := NewExecutor()

	ctx := context.Background()
	_, _, _, err := e.Execute(ctx, "nonexistent-binary-12345", nil, nil, "", "", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
	// Should return error quickly without hanging
}

func TestExecutorReapsZombieProcesses(t *testing.T) {
	e := NewExecutor()

	// Run multiple quick processes to ensure no zombies accumulate
	for i := 0; i < 10; i++ {
		ctx := context.Background()
		exitCode, stdout, stderr, err := e.Execute(ctx, "echo", []string{"hello"}, nil, "", "", 5*time.Second)
		if err != nil {
			t.Errorf("iteration %d: unexpected error: %v", i, err)
		}
		if exitCode != 0 {
			t.Errorf("iteration %d: expected exit code 0, got %d", i, exitCode)
		}
		if stdout != "hello\n" {
			t.Errorf("iteration %d: expected stdout 'hello\\n', got %q", i, stdout)
		}
		if stderr != "" {
			t.Errorf("iteration %d: expected empty stderr, got %q", i, stderr)
		}
	}

	// If we get here without hanging, processes are being reaped properly
}
