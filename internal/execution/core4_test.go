package execution

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core4"
)

func newTestAdapter(t *testing.T) *Adapter {
	t.Helper()
	return New(process.NewExecutor(), 10*time.Second)
}

func request(capability, operation string, args map[string]any) *core4.ExecutionRequest {
	return &core4.ExecutionRequest{
		Type:      core4.TypeExecutionRequest,
		ID:        "req_1",
		Timestamp: time.Now().UTC(),
		BodyID:    "body_1",
		Payload: core4.ExecutionRequestPayload{
			ExecutionID: "ex_1",
			Capability:  capability,
			Operation:   operation,
			Arguments:   args,
		},
	}
}

func TestHandleSuccessfulCommand(t *testing.T) {
	a := newTestAdapter(t)
	req := request(CapabilityProcess, OperationRun,
		map[string]any{"command": "echo", "args": []string{"hello"}})
	res := a.Handle(context.Background(), req)

	if res.Validate() != nil {
		t.Fatalf("result failed validation: %v", res.Validate())
	}
	if res.ValidateResponseTo(req) != nil {
		t.Fatalf("result not correlated: %v", res.ValidateResponseTo(req))
	}
	if res.Payload.Status != core4.StatusSuccess {
		t.Fatalf("expected success, got %q", res.Payload.Status)
	}
	if res.Payload.Result["stdout"] != "hello\n" {
		t.Fatalf("unexpected stdout %q", res.Payload.Result["stdout"])
	}
}

func TestHandleUnsupportedCapability(t *testing.T) {
	a := newTestAdapter(t)
	req := request("nonexistent", "run", map[string]any{"command": "echo"})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core4.StatusUnsupported {
		t.Fatalf("expected unsupported, got %q", res.Payload.Status)
	}
}

func TestHandleInvalidCommand(t *testing.T) {
	a := newTestAdapter(t)
	req := request(CapabilityProcess, OperationRun, nil)
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core4.StatusInvalid {
		t.Fatalf("expected invalid, got %q", res.Payload.Status)
	}
}

func TestHandleMissingBinary(t *testing.T) {
	a := newTestAdapter(t)
	req := request(CapabilityProcess, OperationRun,
		map[string]any{"command": "definitely-no-such-binary-xyz"})
	res := a.Handle(context.Background(), req)
	if res.Payload.Status != core4.StatusFailed {
		t.Fatalf("expected failed, got %q", res.Payload.Status)
	}
}

func TestHandleCancelledContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal semantics differ on windows")
	}
	a := newTestAdapter(t)
	req := request(CapabilityProcess, OperationRun,
		map[string]any{"command": "sleep", "args": []string{"30"}})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	res := a.Handle(ctx, req)
	if res.Payload.Status != core4.StatusCancelled {
		t.Fatalf("expected cancelled, got %q", res.Payload.Status)
	}
}
