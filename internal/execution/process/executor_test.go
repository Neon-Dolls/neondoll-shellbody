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

// TestExecutorParentContextCancellation tests that Executor respects parent context cancellation
func TestExecutorParentContextCancellation(t *testing.T) {
	e := NewExecutor()
	// Create a parent context that we can cancel
	parentCtx, cancel := context.WithCancel(context.Background())
	
	// Channel to receive the result
	resultChan := make(chan struct {
		exitCode int
		stdout   string
		stderr   string
		err      error
	}, 1)
	
	// Start the Execute call in a goroutine
	go func() {
		exitCode, stdout, stderr, err := e.Execute(parentCtx, "sleep", []string{"10"}, []string{}, "", "", 0)
		resultChan <- struct {
			exitCode int
			stdout   string
			stderr   string
			err      error
		}{exitCode, stdout, stderr, err}
	}()
	
	// Wait a bit to ensure the process has started
	time.Sleep(100 * time.Millisecond)
	
	// Cancel the parent context
	cancel()
	
	// Wait for the result
	select {
	case result := <-resultChan:
		// Execute should return promptly due to context cancellation
		if result.err == nil {
			t.Fatalf("Expected error due to context cancellation, got none")
		}
		// Should return exit code -1 for context cancellation
		if result.exitCode != -1 {
			t.Fatalf("Expected exit code -1 for context cancellation, got %d", result.exitCode)
		}
		// Error should be context.Canceled
		if result.err != context.Canceled {
			t.Fatalf("Expected error to be context.Canceled, got %v", result.err)
		}
		// Output should be empty since process was killed quickly
		if result.stdout != "" {
			t.Fatalf("Expected empty stdout, got %q", result.stdout)
		}
		if result.stderr != "" {
			t.Fatalf("Expected empty stderr, got %q", result.stderr)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Timed out waiting for Execute to return")
	}
}
