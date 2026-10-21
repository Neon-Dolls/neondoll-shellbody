// Package execution adapts Shell Body's wire-independent execution machinery
// to the canonical Core 4 execution contract (internal/link/core4).
package execution

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core4"
	"github.com/google/uuid"
)

// Shell Body's advertised capability contract. Core 4 routes execution by
// capability/operation/arguments opaquely; Core defines no canonical operation
// names for host process execution, so this is a Body-local contract that Core
// dispatches as-is.
const (
	CapabilityProcess = "process"
	OperationRun      = "run"
)

// Advertised lists the capabilities a shell Body advertises to Core 4.
func Advertised() []core4.Capability {
	return []core4.Capability{
		{
			ID:         CapabilityProcess,
			Operations: []string{OperationRun},
			Available:  true,
		},
	}
}

// Adapter maps canonical execution.request → process executor → canonical
// execution.result.
type Adapter struct {
	executor       *process.Executor
	defaultTimeout time.Duration
}

// New creates an Adapter. defaultTimeout applies when a request omits one.
func New(executor *process.Executor, defaultTimeout time.Duration) *Adapter {
	return &Adapter{executor: executor, defaultTimeout: defaultTimeout}
}

// Handle executes a canonical request and returns a canonical result. It
// never returns a nil result; failures are expressed via result status.
func (a *Adapter) Handle(ctx context.Context, req *core4.ExecutionRequest) *core4.ExecutionResult {
	result := &core4.ExecutionResult{
		Type:          core4.TypeExecutionResult,
		ID:            uuid.NewString(),
		Timestamp:     time.Now().UTC(),
		DollID:        req.DollID,
		BodyID:        req.BodyID,
		CorrelationID: req.ID,
		Payload: core4.ExecutionResultPayload{
			ExecutionID: req.Payload.ExecutionID,
			Status:      core4.StatusSuccess,
		},
	}
	if req.Payload.Capability != CapabilityProcess || req.Payload.Operation != OperationRun {
		result.Payload.Status = core4.StatusUnsupported
		result.Payload.Result = map[string]any{
			"error": fmt.Sprintf("capability %q operation %q not supported",
				req.Payload.Capability, req.Payload.Operation),
		}
		return result
	}

	command, code, ok := requiredString(req.Payload.Arguments["command"])
	if !ok {
		result.Payload.Status = core4.StatusInvalid
		result.Payload.Result = map[string]any{"error": fmt.Sprintf("invalid arguments: %v", code)}
		return result
	}
	args := stringSlice(req.Payload.Arguments["args"])
	env := stringSlice(req.Payload.Arguments["env"])
	dir, _ := optionalString(req.Payload.Arguments["dir"])
	stdin, _ := optionalString(req.Payload.Arguments["stdin"])
	timeout := a.defaultTimeout
	if ts, err := optionalFloat(req.Payload.Arguments["timeout_s"]); err == nil && ts > 0 {
		timeout = time.Duration(ts * float64(time.Second))
	}

	exitCode, stdout, stderr, err := a.executor.Execute(ctx, command, args, env, dir, stdin, timeout)
	switch {
	case err == nil:
		result.Payload.Status = core4.StatusSuccess
	case ctx.Err() != nil:
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Payload.Status = core4.StatusExpired
		} else {
			result.Payload.Status = core4.StatusCancelled
		}
	default:
		result.Payload.Status = core4.StatusFailed
	}
	result.Payload.Result = map[string]any{
		"exit_code": exitCode,
		"stdout":    stdout,
		"stderr":    stderr,
	}
	return result
}

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
		return "", fmt.Errorf("expected string, got %T", v)
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
		return 0, fmt.Errorf("expected number, got %T", v)
	}
}
