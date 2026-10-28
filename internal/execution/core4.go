// Package execution adapts Shell Body's wire-independent execution machinery
// to the canonical Core 4 execution contract (internal/link/core4).
package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core4"
	"github.com/google/uuid"
)

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

	exitCode, stdout, stderr, err := executeWithExecutor(ctx, a.executor, command, args, env, dir, stdin, timeout)
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

// AdvertisedCore4 lists the capabilities a shell Body advertises to Core 4.
func AdvertisedCore4() []core4.Capability {
	return []core4.Capability{
		{
			ID:         CapabilityProcess,
			Operations: []string{OperationRun},
			Available:  true,
		},
	}
}
