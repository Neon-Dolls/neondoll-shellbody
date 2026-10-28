// Package execution provides wire-independent host process execution machinery
// and adapters for Core 4 and Core 5 execution contracts.
package execution

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
)

// Shell Body's advertised capability contract. Core routes execution by
// capability/operation/arguments opaquely; Core defines no canonical operation
// names for host process execution, so this is a Body-local contract that Core
// dispatches as-is.
const (
	CapabilityProcess = "process"
	OperationRun      = "run"
)

func requiredString(v any) (string, string, bool) {
	s, err := stringFrom(v)
	if err != nil {
		return "", err.Error(), false
	}
	if s == "" {
		return "", "command is required", false
	}
	return s, "", true
}

func optionalString(v any) (string, error) { return stringFrom(v) }

func stringFrom(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("expected string, got %T", t)
	}
}

// stringSlice normalizes args/env from arbitrary JSON values.
func stringSlice(v any) []string {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return strings.Fields(t)
	default:
		return nil
	}
}

func optionalFloat(v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case int:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case string:
		if t == "" {
			return 0, nil
		}
		return strconv.ParseFloat(t, 64)
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("expected number, got %T", t)
	}
}

// ExecutionOutcome classifies the result of an execution.
type ExecutionOutcome int

const (
	OutcomeSuccess ExecutionOutcome = iota
	OutcomeFailed                   // process exited non-zero
	OutcomeExpired                  // executor timeout
	OutcomeCancelled
	OutcomeError // other error (binary not found, etc.)
)

// executeWithExecutor runs the process executor with proper context handling
// and maps the result to exit code, stdout, stderr, and outcome classification.
func executeWithExecutor(ctx context.Context, executor *process.Executor, command string, args []string, env []string, dir string, stdin string, timeout time.Duration) (int, string, string, ExecutionOutcome) {
	exitCode, stdout, stderr, err := executor.Execute(ctx, command, args, env, dir, stdin, timeout)
	switch {
	case err == nil:
		if exitCode == 0 {
			return exitCode, stdout, stderr, OutcomeSuccess
		}
		return exitCode, stdout, stderr, OutcomeFailed
	case errors.Is(err, context.DeadlineExceeded):
		return -1, stdout, stderr, OutcomeExpired
	case errors.Is(err, context.Canceled):
		return -1, stdout, stderr, OutcomeCancelled
	default:
		return -1, stdout, stderr, OutcomeError
	}
}
