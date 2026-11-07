package execution

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core5"
)

func newTestCore5Adapter(t *testing.T) *Core5Adapter {
	t.Helper()
	return NewCore5(process.NewExecutor(), 10*time.Second)
}

func core5Request(capability, operation string, args map[string]any) *core5.ExecutionRequest {
	return &core5.ExecutionRequest{
		Type:      core5.TypeExecutionRequest,
		ID:        "req_1",
		Timestamp: time.Now().UTC(),
		DollID:    "doll_1",
		BodyID:    "body_1",
		Payload: core5.ExecutionRequestPayload{
			ExecutionID: "ex_1",
			Capability:  capability,
			Operation:   operation,
			Arguments:   args,
		},
	}
}

func TestCore5HandleSuccessfulCommand(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "args": []string{"hello"}})
	res := a.Handle(context.Background(), req)

	if res.Payload.Status != core5.ExecutionStatusSuccess {
		t.Fatalf("expected success, got %q", res.Payload.Status)
	}
	if res.Payload.Result["stdout"] != "hello\n" {
		t.Fatalf("unexpected stdout %q", res.Payload.Result["stdout"])
	}
}

func TestCore5HandleUnsupportedCapability(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request("nonexistent", "run", map[string]any{"command": "echo"})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusUnsupported {
		t.Fatalf("expected unsupported, got %q", res.Payload.Status)
	}
}

func TestCore5HandleInvalidCommandMissing(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun, nil)
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusInvalid {
		t.Fatalf("expected invalid, got %q", res.Payload.Status)
	}
}

func TestCore5HandleInvalidCommandEmptyString(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": ""})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusInvalid {
		t.Fatalf("expected invalid, got %q", res.Payload.Status)
	}
}

func TestCore5HandleInvalidCommandWrongType(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": 123})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusInvalid {
		t.Fatalf("expected invalid, got %q", res.Payload.Status)
	}
}

func TestCore5HandleInvalidArgsWrongType(t *testing.T) {
	a := newTestCore5Adapter(t)
	// args as a number should be invalid
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "args": 123})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusInvalid {
		t.Fatalf("expected invalid, got %q", res.Payload.Status)
	}
}

func TestCore5HandleInvalidArgsElementWrongType(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "args": []any{"valid", 123}})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusInvalid {
		t.Fatalf("expected invalid, got %q", res.Payload.Status)
	}
}

func TestCore5HandleInvalidEnvWrongType(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "env": "not-an-array"})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusInvalid {
		t.Fatalf("expected invalid, got %q", res.Payload.Status)
	}
}

func TestCore5HandleInvalidEnvElementWrongType(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "env": []any{"VALID=1", 123}})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusInvalid {
		t.Fatalf("expected invalid, got %q", res.Payload.Status)
	}
}

func TestCore5HandleInvalidDirWrongType(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "dir": 123})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusInvalid {
		t.Fatalf("expected invalid, got %q", res.Payload.Status)
	}
}

func TestCore5HandleInvalidStdinWrongType(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "stdin": 123})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusInvalid {
		t.Fatalf("expected invalid, got %q", res.Payload.Status)
	}
}

func TestCore5HandleInvalidTimeoutNegative(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "timeout_s": -5})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusInvalid {
		t.Fatalf("expected invalid, got %q", res.Payload.Status)
	}
}

func TestCore5HandleInvalidTimeoutWrongType(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "timeout_s": "not-a-number"})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusInvalid {
		t.Fatalf("expected invalid, got %q", res.Payload.Status)
	}
}

func TestCore5HandleMissingBinary(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "definitely-no-such-binary-xyz"})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusFailed {
		t.Fatalf("expected failed, got %q", res.Payload.Status)
	}
}

func TestCore5HandleExecutorTimeout(t *testing.T) {
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "sleep", "args": []string{"10"}, "timeout_s": 0.5})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusExpired {
		t.Fatalf("expected expired (executor timeout), got %q", res.Payload.Status)
	}
	if res.Payload.Result["exit_code"] != -1 {
		t.Fatalf("expected exit_code -1 for timeout, got %v", res.Payload.Result["exit_code"])
	}
}

func TestCore5HandleCancelledContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal semantics differ on windows")
	}
	a := newTestCore5Adapter(t)
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "sleep", "args": []string{"30"}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	res := a.Handle(ctx, req)
	if res.Payload.Status != core5.ExecutionStatusCancelled {
		t.Fatalf("expected cancelled, got %q", res.Payload.Status)
	}
}

func TestCore5HandleValidArgsStringSplit(t *testing.T) {
	a := newTestCore5Adapter(t)
	// args as string should be split by Fields
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "args": "hello world"})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusSuccess {
		t.Fatalf("expected success, got %q", res.Payload.Status)
	}
	if res.Payload.Result["stdout"] != "hello world\n" {
		t.Fatalf("unexpected stdout %q", res.Payload.Result["stdout"])
	}
}

func TestCore5HandleValidEnvArrayString(t *testing.T) {
	a := newTestCore5Adapter(t)
	// env as []string is valid
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "sh", "args": []string{"-c", "echo $MY_VAR"}, "env": []string{"MY_VAR=test_value"}})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusSuccess {
		t.Fatalf("expected success, got %q", res.Payload.Status)
	}
	if res.Payload.Result["stdout"] != "test_value\n" {
		t.Fatalf("unexpected stdout %q", res.Payload.Result["stdout"])
	}
}

func TestCore5HandleValidTimeoutInt(t *testing.T) {
	a := newTestCore5Adapter(t)
	// timeout_s as int should be accepted
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "args": []string{"hello"}, "timeout_s": 5})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusSuccess {
		t.Fatalf("expected success, got %q", res.Payload.Status)
	}
}

func TestCore5HandleValidTimeoutFloat64(t *testing.T) {
	a := newTestCore5Adapter(t)
	// timeout_s as float64 should be accepted
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "args": []string{"hello"}, "timeout_s": 5.5})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusSuccess {
		t.Fatalf("expected success, got %q", res.Payload.Status)
	}
}

func TestCore5HandleValidTimeoutStringNumber(t *testing.T) {
	a := newTestCore5Adapter(t)
	// timeout_s as string number should be accepted
	req := core5Request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "args": []string{"hello"}, "timeout_s": "5"})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core5.ExecutionStatusSuccess {
		t.Fatalf("expected success, got %q", res.Payload.Status)
	}
}
