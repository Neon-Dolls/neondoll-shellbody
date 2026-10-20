// Correctness regression tests for terminal session I/O in the Doll Link
// client. These cover the single-outstanding-request contract, atomic sequence
// allocation under contention, bounded-buffer overflow behavior, multiple
// outputs, and pending-response invalidation on session close/change.
// Additional regression test for the terminal lifecycle issue where a command
// produces output in multiple bursts separated by time.
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

// outputEvent constructs a terminal_output session event payload.
func outputEvent(sessionID, line string, seq int) map[string]interface{} {
	return map[string]interface{}{
		"session_id": sessionID,
		"event":      "terminal_output",
		"data":       map[string]interface{}{"line": line, "sequence": seq},
	}
}

// TestTerminalConcurrentInput verifies the single-outstanding-request gate is
// deterministic under contention: concurrent SendTerminalInput calls have
// exactly one success; every other caller gets ErrTerminalRequestOutstanding.
// The atomic sequence allocation is exercised because the slot is reserved and
// sequenced atomically under the same lock.
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

	successes := 0
	for _, e := range errs {
		switch {
		case e == nil:
			successes++
		case errors.Is(e, ErrTerminalRequestOutstanding):
			// expected under single-outstanding
		default:
			t.Fatalf("unexpected concurrent send error: %v", e)
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly 1 successful concurrent send, got %d", successes)
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

// TestTerminalLifecycleMultiBurst verifies that a command producing output in
// multiple bursts separated by time does not permit a second input to interleave
// before completion (i.e., before all bursts are delivered). This enforces the
// explicit request-completion boundary: terminalInFlight remains true until the
// final burst is consumed.
func TestTerminalLifecycleMultiBurst(t *testing.T) {
	h := startTermHarness(t, "sess_l")
	defer h.cancel()

	h.waitSessionActive(t)

	// === FIRST INPUT (multi-burst response) ===
	if err := h.client.SendTerminalInput(h.ctx, "first"); err != nil {
		t.Fatalf("SendTerminalInput first: %v", err)
	}
	h.waitInput(t) // peer saw the input

	// Deliver first burst of output (two lines)
	h.deliver(t, link.TypeSessionEvent, outputEvent("sess_l", "burst1-line1\n", 0))
	h.deliver(t, link.TypeSessionEvent, outputEvent("sess_l", "burst1-line2\n", 1))

	// Attempt a SECOND input while the first is still considered in flight
	// (we have not yet drained the delivered bursts). This must be rejected.
	err2 := h.client.SendTerminalInput(h.ctx, "second")
	if !errors.Is(err2, ErrTerminalRequestOutstanding) {
		t.Fatalf("expected second send to be rejected while first is in flight, got: %v", err2)
	}

	// Now deliver the second burst (one line) after a pause to simulate
	// processing delay.
	time.Sleep(10 * time.Millisecond) // simulate gap between bursts
	h.deliver(t, link.TypeSessionEvent, outputEvent("sess_l", "burst2-line1\n", 2))

	// Drain all three lines; only after the third is consumed should
	// terminalInFlight become false, permitting a new input.
	var lines []string
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		line, err := h.client.ReceiveTerminalResponse(ctx)
		cancel()
		if err != nil {
			t.Fatalf("ReceiveTerminalResponse[%d]: %v", i, err)
		}
		lines = append(lines, line)
	}
	want := []string{"burst1-line1\n", "burst1-line2\n", "burst2-line1\n"}
	if !equalSlice(lines, want) {
		t.Fatalf("unexpected terminal output lines: got %v, want %v", lines, want)
	}

	// After draining, a new input should succeed.
	if err := h.client.SendTerminalInput(h.ctx, "third"); err != nil {
		t.Fatalf("SendTerminalInput third after drain: %v", err)
	}
	h.waitInput(t) // peer saw the third input

	// Deliver a single line for the third command.
	h.deliver(t, link.TypeSessionEvent, outputEvent("sess_l", "third-line1\n", 0))

	// Drain it.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	line, err := h.client.ReceiveTerminalResponse(ctx)
	cancel()
	if err != nil {
		t.Fatalf("ReceiveTerminalResponse third: %v", err)
	}
	if line != "third-line1\n" {
		t.Fatalf("third output line = %q, want %q", line, "third-line1\n")
	}
}

// equalSlice reports whether a and b contain the same strings in the same order.