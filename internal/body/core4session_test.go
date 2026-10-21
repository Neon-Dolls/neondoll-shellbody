package body

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/execution/process"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core4"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// wsCore stands in for Core 4 on the other end of /ws: it requires the
// canonical body.capabilities advertisement, then issues an execution.request
// and asserts the correlated execution.result.
type wsCore struct {
	upgraded func(w http.ResponseWriter) (*websocket.Conn, error)
	t        *testing.T
}

func (h *wsCore) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.t.Errorf("upgrade: %v", err)
		return
	}
	defer conn.Close()

	// 1. Expect the Body's capability advertisement.
	var adv core4.CapabilitiesAdvertisement
	if err := readJSON(conn, &adv); err != nil {
		h.t.Errorf("read advertisement: %v", err)
		return
	}
	if err := adv.Validate(); err != nil {
		h.t.Errorf("advertisement failed core4 validation: %v", err)
		return
	}
	if adv.BodyID != "body_test" {
		h.t.Errorf("advertisement body_id = %q", adv.BodyID)
	}
	hasProcess := false
	for _, c := range adv.Payload.Capabilities {
		if c.ID == "process" && len(c.Operations) == 1 && c.Operations[0] == "run" {
			hasProcess = true
		}
	}
	if !hasProcess {
		h.t.Error("advertisement missing process/run capability")
		return
	}

	// 2. Issue a canonical execution.request.
	req := &core4.ExecutionRequest{
		Type:      core4.TypeExecutionRequest,
		ID:        "req_test",
		Timestamp: time.Now().UTC(),
		BodyID:    adv.BodyID,
		Payload: core4.ExecutionRequestPayload{
			ExecutionID: "ex_test",
			Capability:  "process",
			Operation:   "run",
			Arguments: map[string]any{
				"command": "echo",
				"args":    []string{"pong"},
			},
		},
	}
	if err := conn.WriteJSON(req); err != nil {
		h.t.Errorf("write request: %v", err)
		return
	}

	// 3. Assert the correlated execution.result.
	var res core4.ExecutionResult
	if err := readJSON(conn, &res); err != nil {
		h.t.Errorf("read result: %v", err)
		return
	}
	if err := res.Validate(); err != nil {
		h.t.Errorf("result failed core4 validation: %v", err)
	}
	if err := res.ValidateResponseTo(req); err != nil {
		h.t.Errorf("result not correlated to request: %v", err)
	}
	if res.Payload.Status != core4.StatusSuccess {
		h.t.Errorf("expected success, got %q", res.Payload.Status)
	}
	if res.Payload.Result["stdout"] != "pong\n" {
		h.t.Errorf("unexpected stdout %q", res.Payload.Result["stdout"])
	}
}

func readJSON(conn *websocket.Conn, dst any) error {
	_, raw, err := conn.ReadMessage()
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func TestCore4SessionRoundTrip(t *testing.T) {
	handler := &wsCore{t: t}
	srv := httptest.NewServer(handler)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):] + "/ws"

	sess, err := DialCore4(wsURL, "body_test", execution.New(process.NewExecutor(), 5*time.Second))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := sess.AdvertiseCapabilities(ctx, execution.Advertised()); err != nil {
		t.Fatalf("advertise: %v", err)
	}

	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = sess.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("run loop did not complete in time")
	}
	wg.Wait()
}

func TestAdvertiseCapabilitiesValidatesBeforeSend(t *testing.T) {
	sess := &Core4Session{bodyID: "body_test"}
	// Empty capability list with a non-empty BodyID still needs an ID before
	// it can be sent; guard against advertising an invalid envelope.
	adv := core4.NewCapabilitiesAdvertisement(sess.bodyID, nil)
	adv.ID = "adv_x"
	if err := adv.Validate(); err != nil {
		t.Fatalf("zero-capability advertisement should validate: %v", err)
	}
}
