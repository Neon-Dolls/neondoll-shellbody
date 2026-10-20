// Correctness regression tests for terminal session I/O in the Doll Link
// client. These cover the stream-of-events model (successive inputs and
// interleaved output within one session), atomic sequence allocation under
// contention, bounded-buffer overflow behavior, multiple outputs, and
// pending-response invalidation on session close/change.
package body

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/link"
)

// termHarness runs a client over the fake transport and drives the peer side.
type termHarness struct {
	ft            *fakeTransport
	client        *Client
	ctx           context.Context
	cancel        context.CancelFunc
	clientDone    chan error
	sessionOpened chan struct{}
	peerErr       chan error
	// sendToClient lets the test push envelopes into the client's receive path.
	sendToClient func(env link.Envelope, payload interface{})
	// nextPeerAction is set by the peer goroutine after it reads the client's
	// first terminal_input, so the test knows the input made it out.
	inputSeen chan struct{}
}

// startTermHarness performs the handshake and opens a terminal session, then
// returns a harness whose peer goroutine is parked so the test can drive the
// exchange. sendToClient wraps an interface{} payload into an envelope of the
// desired type and delivers it to the client run loop.
func startTermHarness(t *testing.T, sessionID string) *termHarness {
	t.Helper()
	ft := NewFakeTransport()
	client := NewClient(ft, "test-body-id")

	h := &termHarness{
		ft:            ft,
		client:        client,
		sessionOpened: make(chan struct{}),
		peerErr:       make(chan error, 4),
		inputSeen:     make(chan struct{}, 16),
	}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.clientDone = make(chan error, 1)

	// Peer goroutine: handshake, open session, then hand control back.
	go func() {
		data := <-ft.sendCh // body.hello
		var _ link.Envelope
		_ = data
		helloPayload, _ := json.Marshal(map[string]interface{}{
			"doll_link_version":     1,
			"body_contract_version": 1,
		})
		hc, _ := json.Marshal(link.Envelope{
			Type: link.TypeCoreHello, ID: "evt_ch", BodyID: "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339), Payload: helloPayload,
		})
		ft.recvCh <- hc
		<-ft.sendCh // body.capabilities
		<-ft.sendCh // body.ready
		opened, _ := json.Marshal(map[string]interface{}{
			"session_id": sessionID, "capability": "terminal",
		})
		od, _ := json.Marshal(link.Envelope{
			Type: link.TypeSessionOpened, ID: "evt_opened", BodyID: "test-body-id",
			Timestamp: time.Now().UTC().Format(time.RFC3339), Payload: opened,
		})
		ft.recvCh <- od
		close(h.sessionOpened)

		// Park reading client->peer (terminal_input events). Forward each
		// session.event observed to inputSeen so tests can synchronize.
		for {
			select {
			case <-h.ctx.Done():
				return
			case data := <-ft.sendCh:
				var env link.Envelope
				if err := json.Unmarshal(data, &env); err != nil {
					h.peerErr <- fmt.Errorf("peer unmarshal: %w", err)
					return
				}
				select {
				case h.inputSeen <- struct{}{}:
				default:
				}
			}
		}
	}()

	go func() { h.clientDone <- client.Run(h.ctx) }()
	return h
}

// deliver pushes an envelope (with given type + payload) to the client and
// blocks until the client run loop has consumed it from the transport.
func (h *termHarness) deliver(t *testing.T, mtype link.MessageType, payload interface{}) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	env, err := json.Marshal(link.Envelope{
		Type: mtype, ID: "evt_x", BodyID: "test-body-id",
		Timestamp: time.Now().UTC().Format(time.RFC3339), Payload: raw,
	})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	select {
	case h.ft.recvCh <- env:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out delivering %s", mtype)
	}
}

// waitBufferState blocks until the terminal response buffer holds exactly n
// lines OR the overflow flag is latched to expectOverflow, and returns the
// observed values. Callers use it after a batch of deliveries to synchronize on
// the run loop having processed them all, independent of transport buffering.
func (h *termHarness) waitBufferState(t *testing.T, n int, expectOverflow bool) (gotLen int, gotOverflow bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.client.terminalMu.Lock()
		gotOverflow = h.client.terminalOverflowed
		gotLen = len(h.client.terminalResponseCh)
		h.client.terminalMu.Unlock()
		if gotOverflow == expectOverflow && gotLen == n {
			return gotLen, gotOverflow
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("buffer never reached state len=%d overflow=%v (got len=%d overflow=%v)", n, expectOverflow, gotLen, gotOverflow)
	return gotLen, gotOverflow
}

// waitSessionActive blocks until the client has actually processed
// session.opened (its internal sessionOpened flag is set), not merely until the
// peer enqueued the open message.
func (h *termHarness) waitSessionActive(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.client.mu.RLock()
		ok := h.client.sessionOpened
		h.client.mu.RUnlock()
		if ok {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("client never processed session.opened")
}

// waitInput blocks until the peer has seen a terminal_input from the client.
func (h *termHarness) waitInput(t *testing.T) {
	t.Helper()
	select {
	case <-h.inputSeen:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for peer to see terminal_input")
	}
}

// waitNInputs blocks until the peer has observed exactly n terminal_input
// events. It is used after concurrent sends to confirm every event reached the
// transport in the stream model.
func (h *termHarness) waitNInputs(n int) error {
	deadline := time.After(5 * time.Second)
	for seen := 0; seen < n; {
		select {
		case <-h.inputSeen:
			seen++
		case <-deadline:
			return fmt.Errorf("peer saw %d of %d terminal_input events", seen, n)
		}
	}
	return nil
}

// outputEvent constructs a terminal_output session event payload.
func outputEvent(sessionID, line string, seq int) map[string]interface{} {
	return map[string]interface{}{
		"session_id": sessionID,
		"event":      "terminal_output",
		"data":       map[string]interface{}{"line": line, "sequence": seq},
	}
}

// TestTerminalConcurrentInput verifies that concurrent SendTerminalInput calls
// all succeed: terminal input is a stream of independent events, so there is no
// single-outstanding gate to contend for. Each concurrent caller gets its own
// atomically-allocated sequence number and its own delivered event, so no caller
// is rejected and no sequence collides.
func TestTerminalConcurrentInput(t *testing.T) {
	h := startTermHarness(t, "sess_c")
	defer h.cancel()

	h.waitSessionActive(t)

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = h.client.SendTerminalInput(h.ctx, fmt.Sprintf("cmd-%d", i))
		}(i)
	}
	wg.Wait()

	// Every concurrent send must succeed — there is no in-flight gate.
	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent send %d failed: %v", i, e)
		}
	}

	// All `goroutines` events must have reached the peer.
	if err := h.waitNInputs(goroutines); err != nil {
		t.Fatalf("peer did not receive all %d inputs: %v", goroutines, err)
	}

	// The atomic allocator issued exactly as many unique sequence numbers as
	// there were concurrent senders.
	if got := h.client.nextTerminalSequence.Load(); got != goroutines {
		t.Fatalf("expected %d sequences allocated, got %d", goroutines, got)
	}
}

// TestTerminalMultipleOutputs verifies that a single request may yield several
// terminal_output lines, all delivered in arrival order.
func TestTerminalMultipleOutputs(t *testing.T) {
	h := startTermHarness(t, "sess_m")
	defer h.cancel()

	h.waitSessionActive(t)
	if err := h.client.SendTerminalInput(h.ctx, "cmd"); err != nil {
		t.Fatalf("SendTerminalInput: %v", err)
	}
	h.waitInput(t)

	want := []string{"one\n", "two\n", "three\n"}
	for i, line := range want {
		h.deliver(t, link.TypeSessionEvent, outputEvent("sess_m", line, i))
	}
	h.waitBufferState(t, len(want), false)

	for i, w := range want {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		got, err := h.client.ReceiveTerminalResponse(ctx)
		cancel()
		if err != nil {
			t.Fatalf("ReceiveTerminalResponse[%d]: %v", i, err)
		}
		if got != w {
			t.Fatalf("output[%d] = %q, want %q", i, got, w)
		}
	}
}

// TestTerminalBufferOverflow verifies that a saturated response buffer is never
// silently dropped: the buffer fills to capacity and then overflows are
// surfaced as ErrTerminalResponseOverflow rather than dropped.
func TestTerminalBufferOverflow(t *testing.T) {
	h := startTermHarness(t, "sess_o")
	defer h.cancel()

	h.waitSessionActive(t)
	if err := h.client.SendTerminalInput(h.ctx, "cmd"); err != nil {
		t.Fatalf("SendTerminalInput: %v", err)
	}
	h.waitInput(t)

	// Fill the buffer past capacity without draining.
	const over = terminalResponseBufferSize + 1
	for i := 0; i < over; i++ {
		h.deliver(t, link.TypeSessionEvent, outputEvent("sess_o", fmt.Sprintf("L%d\n", i), i))
	}
	// Wait until the run loop has processed all deliveries: exactly capacity
	// lines retained and the overflow flag latched.
	h.waitBufferState(t, terminalResponseBufferSize, true)

	// The first receive surfaces the overflow deterministically; the rest must
	// be draining the retained lines, not be dropped lines.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := h.client.ReceiveTerminalResponse(ctx)
	cancel()
	if !errors.Is(err, ErrTerminalResponseOverflow) {
		t.Fatalf("expected ErrTerminalResponseOverflow from saturated buffer, got: %v", err)
	}

	// After the overflow is surfaced, the retained lines remain deliverable.
	first, err := h.client.ReceiveTerminalResponse(context.Background())
	if err != nil {
		t.Fatalf("expected retained line after overflow, got error: %v", err)
	}
	if first != "L0\n" {
		t.Fatalf("first retained line = %q, want %q", first, "L0\n")
	}
}

// TestTerminalSessionReplacement verifies that closing a session invalidates
// pending responses: a goroutine blocked in ReceiveTerminalResponse wakes with
// ErrTerminalSessionClosed, and a subsequent send is rejected until a new
// session opens.
func TestTerminalSessionReplacement(t *testing.T) {
	h := startTermHarness(t, "sess_a")
	defer h.cancel()

	h.waitSessionActive(t)
	if err := h.client.SendTerminalInput(h.ctx, "cmd"); err != nil {
		t.Fatalf("SendTerminalInput: %v", err)
	}
	h.waitInput(t)
	// The request is now in flight (response pending).

	// Block a receiver; it must be woken by session close.
	type res struct {
		line string
		err  error
	}
	rcv := make(chan res, 1)
	go func() {
		line, err := h.client.ReceiveTerminalResponse(h.ctx)
		rcv <- res{line, err}
	}()

	// Let the receive actually block.
	time.Sleep(50 * time.Millisecond)

	// Close the session: pending responses must be invalidated.
	h.deliver(t, link.TypeSessionClose, map[string]interface{}{"session_id": "sess_a"})

	select {
	case r := <-rcv:
		if r.err == nil {
			t.Fatalf("expected session-close error, got line %q", r.line)
		}
		if !errors.Is(r.err, ErrTerminalSessionClosed) {
			t.Fatalf("expected ErrTerminalSessionClosed, got %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("blocked ReceiveTerminalResponse not woken by session close")
	}

	// A new send must be rejected (no active session).
	err := h.client.SendTerminalInput(h.ctx, "again")
	if !errors.Is(err, ErrTerminalSessionNotOpened) &&
		!strings.Contains(err.Error(), "session not opened") {
		t.Fatalf("expected session-not-opened error after close, got: %v", err)
	}
}

// TestTerminalStreamSuccessiveInputs verifies the stream model: successive
// terminal inputs are sent independently within one session without any
// completion gate. The protocol carries no per-command completion signal, so a
// second input is accepted immediately, and output for both inputs arrives in
// arrival order through the shared FIFO.
func TestTerminalStreamSuccessiveInputs(t *testing.T) {
	h := startTermHarness(t, "sess_s")
	defer h.cancel()

	h.waitSessionActive(t)

	// Send three inputs back-to-back while the session stays open. Each must
	// succeed immediately — there is no single-outstanding gate to block them.
	for _, in := range []string{"one", "two", "three"} {
		if err := h.client.SendTerminalInput(h.ctx, in); err != nil {
			t.Fatalf("SendTerminalInput(%q) must succeed in stream model: %v", in, err)
		}
	}
	if err := h.waitNInputs(3); err != nil {
		t.Fatalf("wait for 3 inputs: %v", err)
	}

	// Deliver output for all three in arrival order; the client must surface
	// every line, in order, regardless of which input it "belongs" to.
	for i, out := range []string{"out-one\n", "out-two\n", "out-three\n"} {
		h.deliver(t, link.TypeSessionEvent, outputEvent("sess_s", out, i))
	}
	seen, err := h.collectOutput(3, 5*time.Second)
	if err != nil {
		t.Fatalf("collect successive output: %v", err)
	}
	want := []string{"out-one\n", "out-two\n", "out-three\n"}
	if !equalSlice(seen, want) {
		t.Fatalf("successive output = %v, want %v", seen, want)
	}
}

// TestTerminalStreamInterleavedOutput verifies that output from multiple inputs
// interleaved over the wire is delivered strictly in arrival order — the client
// never reorders, drops, or attributes a line to a specific input (the protocol
// has no correlation token).
func TestTerminalStreamInterleavedOutput(t *testing.T) {
	h := startTermHarness(t, "sess_i")
	defer h.cancel()

	h.waitSessionActive(t)

	// Two inputs outstanding; their output interleaves (a1, b1, a2, b2).
	if err := h.client.SendTerminalInput(h.ctx, "a"); err != nil {
		t.Fatalf("SendTerminalInput(a): %v", err)
	}
	if err := h.client.SendTerminalInput(h.ctx, "b"); err != nil {
		t.Fatalf("SendTerminalInput(b): %v", err)
	}
	if err := h.waitNInputs(2); err != nil {
		t.Fatalf("wait for 2 inputs: %v", err)
	}

	for i, out := range []string{"a1\n", "b1\n", "a2\n", "b2\n"} {
		h.deliver(t, link.TypeSessionEvent, outputEvent("sess_i", out, i))
	}
	seen, err := h.collectOutput(4, 5*time.Second)
	if err != nil {
		t.Fatalf("collect interleaved output: %v", err)
	}
	want := []string{"a1\n", "b1\n", "a2\n", "b2\n"}
	if !equalSlice(seen, want) {
		t.Fatalf("interleaved output = %v, want %v", seen, want)
	}
}

// TestTerminalReconnect verifies reconnect behavior: after a session closes, a
// freshly opened session accepts terminal input again, and pending output from
// the previous session is invalidated rather than delivered to the new one.
func TestTerminalReconnect(t *testing.T) {
	h := startTermHarness(t, "sess_r1")
	defer h.cancel()

	h.waitSessionActive(t)
	if err := h.client.SendTerminalInput(h.ctx, "first-session"); err != nil {
		t.Fatalf("SendTerminalInput in first session: %v", err)
	}
	if err := h.waitNInputs(1); err != nil {
		t.Fatalf("peer did not see first-session input: %v", err)
	}

	// Close the first session; outstanding receive must wake with a close error.
	h.deliver(t, link.TypeSessionClose, map[string]interface{}{"session_id": "sess_r1"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, err := h.client.ReceiveTerminalResponse(ctx)
	cancel()
	if !errors.Is(err, ErrTerminalSessionClosed) {
		t.Fatalf("expected ErrTerminalSessionClosed after close, got: %v", err)
	}

	// A send while no session is open must be rejected.
	err = h.client.SendTerminalInput(h.ctx, "orphan")
	if !errors.Is(err, ErrTerminalSessionNotOpened) && !strings.Contains(err.Error(), "session not opened") {
		t.Fatalf("expected session-not-opened after close, got: %v", err)
	}

	// Reconnect: a new session opens with a fresh session ID.
	h.deliver(t, link.TypeSessionOpened, map[string]interface{}{
		"session_id": "sess_r2", "capability": "terminal",
	})
	h.waitSessionActive(t)

	// The new session must accept input and deliver output in arrival order.
	if err := h.client.SendTerminalInput(h.ctx, "second-session"); err != nil {
		t.Fatalf("SendTerminalInput in reconnected session: %v", err)
	}
	if err := h.waitNInputs(1); err != nil {
		t.Fatalf("peer did not see reconnected-session input: %v", err)
	}
	h.deliver(t, link.TypeSessionEvent, outputEvent("sess_r2", "hello-again\n", 0))
	line, err := h.client.ReceiveTerminalResponse(h.ctx)
	if err != nil {
		t.Fatalf("ReceiveTerminalResponse in reconnected session: %v", err)
	}
	if line != "hello-again\n" {
		t.Fatalf("reconnected output = %q, want %q", line, "hello-again\n")
	}
}

// equalSlice reports whether a and b contain the same strings in the same order.
// (Implemented in mobility_test.go within this package.)

// collectOutput drains n terminal output lines from ReceiveTerminalResponse,
// returning them in arrival order or the first error encountered.
func (h *termHarness) collectOutput(n int, timeout time.Duration) ([]string, error) {
	var out []string
	deadline := time.Now().Add(timeout)
	for len(out) < n {
		ctx, cancel := context.WithTimeout(context.Background(), time.Until(deadline))
		line, err := h.client.ReceiveTerminalResponse(ctx)
		cancel()
		if err != nil {
			return out, fmt.Errorf("ReceiveTerminalResponse: %w", err)
		}
		out = append(out, line)
	}
	return out, nil
}
