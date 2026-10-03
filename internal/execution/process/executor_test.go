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
