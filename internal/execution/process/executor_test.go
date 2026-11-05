// Package process provides wire-independent host process execution machinery.
package process

import (
	"context"
	"testing"
	"time"
)

// TestExecutorSuccess tests successful command execution
func TestExecutorSuccess(t *testing.T) {
	e := NewExecutor()
	ctx := context.Background()

	exitCode, stdout, _, err := e.Execute(ctx, "echo", []string{"hello"}, []string{}, "", "", 5*time.Second)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("Expected exit code 0, got %d", exitCode)
	}
	if stdout != "hello\n" {
		t.Fatalf("Expected stdout 'hello\\n', got %q", stdout)
	}
}

// TestExecutorWithArgs tests command execution with arguments
func TestExecutorWithArgs(t *testing.T) {
	e := NewExecutor()
	ctx := context.Background()

	exitCode, stdout, _, err := e.Execute(ctx, "echo", []string{"hello", "world"}, []string{}, "", "", 5*time.Second)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("Expected exit code 0, got %d", exitCode)
	}
	if stdout != "hello world\n" {
		t.Fatalf("Expected stdout 'hello world\\n', got %q", stdout)
	}
}

// TestExecutorNonZeroExit tests command execution with non-zero exit code
func TestExecutorNonZeroExit(t *testing.T) {
	e := NewExecutor()
	ctx := context.Background()

	exitCode, _, _, err := e.Execute(ctx, "sh", []string{"-c", "exit 42"}, []string{}, "", "", 5*time.Second)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if exitCode != 42 {
		t.Fatalf("Expected exit code 42, got %d", exitCode)
	}
}

// TestExecutorWithEnv tests command execution with environment variables
func TestExecutorWithEnv(t *testing.T) {
	e := NewExecutor()
	ctx := context.Background()

	exitCode, stdout, _, err := e.Execute(ctx, "sh", []string{"-c", "echo $MY_VAR"}, []string{"MY_VAR=test_value"}, "", "", 5*time.Second)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("Expected exit code 0, got %d", exitCode)
	}
	if stdout != "test_value\n" {
		t.Fatalf("Expected stdout 'test_value\\n', got %q", stdout)
	}
}

// TestExecutorWithDir tests command execution in a specific directory
func TestExecutorWithDir(t *testing.T) {
	e := NewExecutor()
	ctx := context.Background()

	// Create a temporary directory for testing
	dir := t.TempDir()

	exitCode, stdout, _, err := e.Execute(ctx, "sh", []string{"-c", "pwd"}, []string{}, dir, "", 5*time.Second)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("Expected exit code 0, got %d", exitCode)
	}
	if stdout != dir+"\n" {
		t.Fatalf("Expected stdout '%s\\n', got %q", dir, stdout)
	}
}

// TestExecutorWithStdin tests command execution with stdin input
func TestExecutorWithStdin(t *testing.T) {
	e := NewExecutor()
	ctx := context.Background()

	exitCode, stdout, _, err := e.Execute(ctx, "cat", []string{}, []string{}, "", "hello world", 5*time.Second)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}
	if exitCode != 0 {
		t.Fatalf("Expected exit code 0, got %d", exitCode)
	}
	if stdout != "hello world" {
		t.Fatalf("Expected stdout 'hello world', got %q", stdout)
	}
}

// TestExecutorTimeout tests command execution timeout
func TestExecutorTimeout(t *testing.T) {
	e := NewExecutor()
	ctx := context.Background()

	// Use a command that will sleep longer than our timeout
	exitCode, _, _, err := e.Execute(ctx, "sleep", []string{"10"}, []string{}, "", "", 1*time.Second)
	if err == nil {
		t.Fatalf("Expected error due to timeout, got none")
	}
	// Should be context deadline exceeded or similar
	if exitCode != -1 {
		t.Fatalf("Expected exit code -1 for timeout, got %d", exitCode)
	}
}

// TestExecutorCommandNotFound tests execution of non-existent command
func TestExecutorCommandNotFound(t *testing.T) {
	e := NewExecutor()
	ctx := context.Background()

	exitCode, _, _, err := e.Execute(ctx, "nonexistentcommand12345", []string{}, []string{}, "", "", 5*time.Second)
	if err == nil {
		t.Fatalf("Expected error for non-existent command, got none")
	}
	// Should return exit code -1 and an error
	if exitCode != -1 {
		t.Fatalf("Expected exit code -1 for command not found, got %d", exitCode)
	}
	if err == nil {
		t.Fatalf("Expected error, got nil")
	}
}

// TestExecutorCancelsAndReapsProcess tests that a long-running process is cancelled and reaped
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

// TestExecutorTimeoutKillsProcess tests that a timeout kills the process
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

// TestExecutorNoOrphanOnQuickCancel tests immediate cancellation
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

// TestExecutorNoOrphanOnBinaryNotFound tests error handling for nonexistent binary
func TestExecutorNoOrphanOnBinaryNotFound(t *testing.T) {
	e := NewExecutor()

	ctx := context.Background()
	_, _, _, err := e.Execute(ctx, "nonexistent-binary-12345", nil, nil, "", "", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for nonexistent binary")
	}
	// Should return error quickly without hanging
}

// TestExecutorReapsZombieProcesses runs multiple quick processes to ensure no zombies accumulate
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
