// Package execution adapts Shell Body's wire-independent execution machinery
// to the canonical Core 5 execution contract (internal/link/core5).
package execution

import (
	"context"
	"fmt"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core5"
	"github.com/google/uuid"
)

// Core5Adapter maps canonical execution.request → process executor → canonical
// execution.result.
type Core5Adapter struct {
	executor       *process.Executor
	defaultTimeout time.Duration
}

// NewCore5 creates a Core5Adapter. defaultTimeout applies when a request omits one.
func NewCore5(executor *process.Executor, defaultTimeout time.Duration) *Core5Adapter {
	return &Core5Adapter{executor: executor, defaultTimeout: defaultTimeout}
}

// Handle executes a canonical request and returns a canonical result. It
// never returns a nil result; failures are expressed via result status.
func (a *Core5Adapter) Handle(ctx context.Context, req *core5.ExecutionRequest) *core5.ExecutionResult {
	result := &core5.ExecutionResult{
		Type:          core5.TypeExecutionResult,
		ID:            uuid.NewString(),
		Timestamp:     time.Now().UTC(),
		DollID:        req.DollID,
		BodyID:        req.BodyID,
		CorrelationID: req.ID,
		Payload: core5.ExecutionResultPayload{
			ExecutionID: req.Payload.ExecutionID,
			Status:      core5.ExecutionStatusSuccess,
		},
	}
	if req.Payload.Capability != CapabilityProcess || req.Payload.Operation != OperationRun {
		result.Payload.Status = core5.ExecutionStatusUnsupported
		result.Payload.Result = map[string]any{
			"error": fmt.Sprintf("capability %q operation %q not supported",
				req.Payload.Capability, req.Payload.Operation),
		}
		return result
	}

	// Strict argument validation - reject malformed arguments
	command, code, ok := requiredString(req.Payload.Arguments["command"])
	if !ok {
		result.Payload.Status = core5.ExecutionStatusInvalid
		result.Payload.Result = map[string]any{"error": fmt.Sprintf("invalid arguments: %v", code)}
		return result
	}

	// Validate args
	argsRaw, ok := req.Payload.Arguments["args"]
	if argsRaw != nil {
		switch v := argsRaw.(type) {
		case []string:
			// valid
		case []any:
			for i, item := range v {
				if _, ok := item.(string); !ok {
					result.Payload.Status = core5.ExecutionStatusInvalid
					result.Payload.Result = map[string]any{"error": fmt.Sprintf("invalid arguments: args[%d] must be string, got %T", i, item)}
					return result
				}
			}
		case string:
			// will be split by Fields
		default:
			result.Payload.Status = core5.ExecutionStatusInvalid
			result.Payload.Result = map[string]any{"error": fmt.Sprintf("invalid arguments: args must be []string, []any, or string, got %T", v)}
			return result
		}
	}
	args := stringSlice(argsRaw)

	// Validate env
	envRaw, ok := req.Payload.Arguments["env"]
	if envRaw != nil {
		switch v := envRaw.(type) {
		case []string:
			// valid
		case []any:
			for i, item := range v {
				if _, ok := item.(string); !ok {
					result.Payload.Status = core5.ExecutionStatusInvalid
					result.Payload.Result = map[string]any{"error": fmt.Sprintf("invalid arguments: env[%d] must be string, got %T", i, item)}
					return result
				}
			}
		default:
			result.Payload.Status = core5.ExecutionStatusInvalid
			result.Payload.Result = map[string]any{"error": fmt.Sprintf("invalid arguments: env must be []string or []any, got %T", v)}
			return result
		}
	}
	env := stringSlice(envRaw)

	// Validate dir
	dir, err := optionalString(req.Payload.Arguments["dir"])
	if err != nil {
		result.Payload.Status = core5.ExecutionStatusInvalid
		result.Payload.Result = map[string]any{"error": fmt.Sprintf("invalid arguments: %v", err)}
		return result
	}

	// Validate stdin
	stdin, err := optionalString(req.Payload.Arguments["stdin"])
	if err != nil {
		result.Payload.Status = core5.ExecutionStatusInvalid
		result.Payload.Result = map[string]any{"error": fmt.Sprintf("invalid arguments: %v", err)}
		return result
	}

	// Validate timeout_s
	timeout := a.defaultTimeout
	if ts, err := optionalFloat(req.Payload.Arguments["timeout_s"]); err != nil {
		result.Payload.Status = core5.ExecutionStatusInvalid
		result.Payload.Result = map[string]any{"error": fmt.Sprintf("invalid arguments: %v", err)}
		return result
	} else if ts > 0 {
		timeout = time.Duration(ts * float64(time.Second))
	} else if ts < 0 {
		result.Payload.Status = core5.ExecutionStatusInvalid
		result.Payload.Result = map[string]any{"error": "invalid arguments: timeout_s must be positive"}
		return result
	}

	exitCode, stdout, stderr, outcome := executeWithExecutor(ctx, a.executor, command, args, env, dir, stdin, timeout)
	switch outcome {
	case OutcomeSuccess:
		result.Payload.Status = core5.ExecutionStatusSuccess
	case OutcomeFailed:
		result.Payload.Status = core5.ExecutionStatusFailed
	case OutcomeExpired:
		result.Payload.Status = core5.ExecutionStatusExpired
	case OutcomeCancelled:
		result.Payload.Status = core5.ExecutionStatusCancelled
	case OutcomeError:
		result.Payload.Status = core5.ExecutionStatusFailed
	}
	result.Payload.Result = map[string]any{
		"exit_code": exitCode,
		"stdout":    stdout,
		"stderr":    stderr,
	}
	return result
}

// AdvertisedCore5 lists the capabilities a shell Body advertises to Core 5.
func AdvertisedCore5() []core5.Capability {
	return []core5.Capability{
		{
			ID:         CapabilityProcess,
			Operations: []string{OperationRun},
			Available:  true,
		},
	}
}
