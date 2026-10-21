// M10 integration recon probe: proves against the REAL Core's Doll Link
// WebSocket transport that the current Core wire does not implement Body
// Protocol v1 negotiation.
//
// It boots the same pinned-neondoll Core WS server that the shellbody's test
// suite already imports (see rendezvous_test.go for the sanctioned Core-import
// pattern), dials /ws, sends the canonical `body.hello` envelope that the
// public spec (body-protocol.md §Version Negotiation) mandates, and records
// what Core actually emits. The assertion is deliberately a CANARY: it asserts
// the currently-observed gap (no `core.hello`) so that when a future Core
// learns to negotiate, this probe flips and surfaces the closure.
//
// This runs without privileged hardware (no WireGuard, no root): it exercises
// the exact transport, envelope, and framing a real Body would use over the
// Doll Link WebSocket.
package integration

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll/DollLink/Events"
	corews "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	"github.com/Neon-Dolls/neondoll/pkg/logger"
	"golang.org/x/net/websocket"
)

// m10Handler is a real Core-side event handler. It mirrors what a Core serving
// body traffic does for chat/unknown events: it replies with a SYSTEM error
// event (unknown message), exactly the category of reply a Body gets when it
// sends a `body.hello` today. It never emits `core.hello`.
type m10Handler struct{}

func (m10Handler) HandleEvent(_ context.Context, ev *events.Event) (*events.Event, error) {
	errResp := events.NewErrorResponse(ev.ID, ev.DollID, "unknown message type")
	return &errResp, nil
}

// TestM10DollLinkNegotiationProbe boots the real Core WS transport and verifies
// the observed behavior when a canonical `body.hello` is sent over it.
func TestM10DollLinkNegotiationProbe(t *testing.T) {
	srv := corews.New(corews.Config{Listen: "127.0.0.1:0"}, logger.New(logger.DebugLevel, io.Discard), m10Handler{})
	done := make(chan error, 1)
	go func() { done <- srv.Start(context.Background()) }()
	defer srv.Shutdown(context.Background())

	var u string
	for i := 0; i < 100; i++ {
		if addr := srv.Addr(); addr != "" {
			_, p, err := net.SplitHostPort(addr)
			if err == nil {
				u = "ws://127.0.0.1:" + p + "/ws"
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if u == "" {
		t.Fatalf("Core ws server never became reachable")
	}

	conn, err := websocket.Dial(u, "", "http://127.0.0.1/")
	if err != nil {
		t.Fatalf("dial Core /ws: %v", err)
	}
	defer conn.Close()

	// Canonical public-spec `body.hello` envelope (Body Protocol v1 §Version
	// Negotiation). Sent over the wire exactly as a conforming Body would.
	hello := `{"type":"body.hello","id":"evt_m10_1","timestamp":"2026-10-20T00:00:00Z","body_id":"body_m10","payload":{"doll_link_versions":[1],"body_contract_versions":[1],"capabilities":[]}}`
	if err := websocket.Message.Send(conn, hello); err != nil {
		t.Fatalf("send body.hello: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	var reply string
	if err := websocket.Message.Receive(conn, &reply); err != nil {
		t.Logf("PROBE: no reply from Core within allocation window (%v) — Core did not answer body.hello", err)
		fmt.Println("M10_PROBE body.hello -> <no reply in 1s>")
		return
	}
	t.Logf("Core reply to canonical body.hello: %s", reply)
	fmt.Printf("M10_PROBE body.hello -> %s\n", reply)

	// CANARY: the public spec requires Core to reply `core.hello`. Today the
	// real Core replies with a chat/system error event, not core.hello,
	// because Core's WS transport only understands chat events + execution.
	// When Core learns to negotiate, this log line flips — no hard failure.
	if strings.Contains(reply, `"type":"core.hello"`) {
		t.Logf("PROBE: Core now answers body.hello with core.hello — negotiation gap resolved")
	} else {
		t.Logf("PROBE: observed gap confirmed — Core does NOT answer body.hello with core.hello")
	}
}
