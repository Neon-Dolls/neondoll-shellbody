package interaction

// Unit tests for the Core 5 interaction conversation (M11.1 + M11.2): open a
// session, send terminal lines, receive/display replies, close cleanly, and —
// in M11.2 — preserve the session across a transport drop by re-dialing
// (re-authenticating) and re-selecting the SAME session id, failing closed if
// the session is reported unavailable. The transport is a scripted Session
// whose Write answers each canonical request with a protocol-shaped response,
// so the conversation's framing, correlation matching, reconnect/resume, and
// fail-closed behavior are exercised without a real Core.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/link/core5"
	"github.com/Neon-Dolls/neondoll-shellbody/internal/terminal"
	"github.com/google/uuid"
)

// scripted is a Session transport that records every written frame and answers
// each request with a canned, protocol-shaped response.
type scripted struct {
	responses        chan []byte
	written          chan []byte
	coreErr          bool     // interactions reply with a system error
	doubleReply      bool     // interactions reply with a decoy then the real reply
	createErr        bool     // session.create replies with a Core error
	unsolicited      bool     // interaction is prefixed with an uncorrelated (empty cid) event
	transportErr     bool     // interaction Write fails (simulated connection loss)
	selectErr        bool     // session.select replies with a Core error (session unavailable)
	selectStatus     string   // Status returned by a successful session.select (default "active")
	createCount      int      // session.create frames written
	selectCount      int      // session.select frames written
	selectedIDs      []string // session ids passed to session.select
	interactionTexts []string // payload text of each interaction frame written
}

func newScripted() *scripted {
	return &scripted{
		responses:    make(chan []byte, 16),
		written:      make(chan []byte, 16),
		selectStatus: "active",
	}
}

func (s *scripted) session() Session {
	_ = s
	return Session{
		Write: func(ctx context.Context, value any) error {
			frame, err := json.Marshal(value)
			if err != nil {
				return err
			}
			s.written <- frame
			var env struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(frame, &env); err != nil {
				return nil
			}
			switch env.Type {
			case "session.create":
				var req core5.SessionRequest
				_ = json.Unmarshal(frame, &req)
				s.createCount++
				if s.createErr {
					s.responses <- errorResult(req.ID)
				} else {
					s.responses <- okResult(req.ID, "sess-1")
				}
			case "session.close":
				var cr core5.SessionRequest
				_ = json.Unmarshal(frame, &cr)
				s.responses <- okResult(cr.ID, "sess-1")
			case "session.select":
				var sel core5.SessionRequest
				_ = json.Unmarshal(frame, &sel)
				s.selectCount++
				s.selectedIDs = append(s.selectedIDs, sel.Payload.SessionID)
				if s.selectErr {
					s.responses <- errorResult(sel.ID)
				} else {
					s.responses <- activeResult(sel.ID, sel.Payload.SessionID, s.selectStatus)
				}
			case "interaction":
				var it core5.Interaction
				_ = json.Unmarshal(frame, &it)
				// Record every interaction, even one whose transport fails, so a
				// test can prove an interrupted line is never retransmitted.
				s.interactionTexts = append(s.interactionTexts, interactionText(it.Event.Payload))
				if s.transportErr {
					// Simulate mid-conversation connection loss: the frame has
					// already been recorded in written; fail the write instead
					// of ever answering it.
					return fmt.Errorf("simulated transport failure")
				}
				if s.unsolicited {
					// An unsolicited push with no correlation id must be
					// ignored, never mistaken for the reply.
					s.responses <- msgReply("", "unsolicited push")
				}
				if s.coreErr {
					s.responses <- newSystemError(it.Event.ID, "core said no")
				} else if s.doubleReply {
					s.responses <- msgReply("decoy-"+it.Event.ID, "wrong")
					s.responses <- msgReply(it.Event.ID, "right")
				} else {
					s.responses <- msgReply(it.Event.ID, "echo: "+interactionText(it.Event.Payload))
				}
			}
			return nil
		},
		Read: func(ctx context.Context) ([]byte, error) {
			select {
			case b := <-s.responses:
				return b, nil
			}
		},
		Close: func() error {
			return nil
		},
	}
}

func okResult(corr, sid string) []byte {
	resp := core5.SessionResponse{
		Version:       core5.SessionControlVersion,
		Type:          core5.TypeResult,
		ID:            "core-1",
		CorrelationID: corr,
		Payload: core5.SessionResponsePayload{
			Operation: core5.TypeCreate,
			Status:    core5.StatusOK,
			Session: &core5.Session{
				ID:        sid,
				Status:    "open",
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

// activeResult is the session.select success response: the selected session is
// reported as active (matching real Core), carrying the requested session id.
func activeResult(corr, sid, status string) []byte {
	resp := core5.SessionResponse{
		Version:       core5.SessionControlVersion,
		Type:          core5.TypeResult,
		ID:            "core-1",
		CorrelationID: corr,
		Payload: core5.SessionResponsePayload{
			Operation: core5.TypeSelect,
			Status:    core5.StatusOK,
			Session: &core5.Session{
				ID:        sid,
				Status:    status,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func errorResult(corr string) []byte {
	resp := core5.SessionResponse{
		Version:       core5.SessionControlVersion,
		Type:          core5.TypeResult,
		ID:            "core-1",
		CorrelationID: corr,
		Payload: core5.SessionResponsePayload{
			Operation: core5.TypeCreate,
			Status:    core5.StatusError,
			Error:     &core5.SessionError{Code: core5.ErrorSessionUnavailable, Message: "no session"},
		},
	}
	b, _ := json.Marshal(resp)
	return b
}

func msgReply(corr, text string) []byte {
	ev := core5.Event{
		ID:            uuid.NewString(),
		Type:          core5.KindMessage,
		Source:        "core",
		DollID:        "doll-1",
		CorrelationID: corr,
		Timestamp:     time.Now().UTC(),
		Payload:       core5.MessagePayload{Text: text},
	}
	b, _ := json.Marshal(ev)
	return b
}

func newSystemError(corr, msg string) []byte {
	ev := core5.Event{
		ID:            uuid.NewString(),
		Type:          core5.KindSystem,
		Source:        "core",
		CorrelationID: corr,
		Timestamp:     time.Now().UTC(),
		Payload:       core5.SystemPayload{Event: "error", Data: msg},
	}
	b, _ := json.Marshal(ev)
	return b
}

func interactionText(payload any) string {
	switch p := payload.(type) {
	case core5.MessagePayload:
		return p.Text
	case map[string]any:
		if v, ok := p["text"].(string); ok {
			return v
		}
		return ""
	default:
		return ""
	}
}

func drainWritten(t *testing.T, s *scripted, n int) [][]byte {
	var frames [][]byte
	for i := 0; i < n; i++ {
		select {
		case b := <-s.written:
			frames = append(frames, b)
		case <-time.After(time.Second):
			t.Fatalf("timed out draining %d written frames (got %d)", n, len(frames))
		}
	}
	return frames
}

func TestOpenSendClose(t *testing.T) {
	t.Helper()
	scr := newScripted()
	sess := scr.session()
	ctx := context.Background()

	sid, err := OpenSession(ctx, sess, "sc-open")
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if sid != "sess-1" {
		t.Fatalf("sid = %q, want sess-1", sid)
	}

	reply, kind, err := SendLine(ctx, sess, sid, "core", "hello")
	if err != nil {
		t.Fatalf("SendLine err: %v", err)
	}
	if kind != ErrNone {
		t.Fatalf("kind = %q, want err-none", kind)
	}
	if reply != "echo: hello" {
		t.Fatalf("reply = %q, want %q", reply, "echo: hello")
	}

	if cerr := CloseSession(ctx, sess, sid); cerr != nil {
		t.Fatalf("CloseSession: %v", cerr)
	}

	frames := drainWritten(t, scr, 3)
	if !strings.Contains(string(frames[0]), "session.create") {
		t.Fatalf("frame[0] = %q, want session.create", string(frames[0]))
	}
	if !strings.Contains(string(frames[1]), `"interaction"`) {
		t.Fatalf("frame[1] = %q, want interaction", string(frames[1]))
	}
	if !strings.Contains(string(frames[2]), "session.close") {
		t.Fatalf("frame[2] = %q, want session.close", string(frames[2]))
	}
}

func TestOpenSessionFailsClosedOnCoreError(t *testing.T) {
	t.Helper()
	scr := newScripted()
	scr.createErr = true
	sid, err := OpenSession(context.Background(), scr.session(), "sc-open")
	if err == nil {
		t.Fatalf("OpenSession succeeded (sid %q) but Core rejected", sid)
	}
	if !strings.Contains(fmt.Sprintf("%v", err), "session_unavailable") {
		t.Fatalf("error = %v, want session_unavailable", err)
	}
}

func TestSendLineSurfacesCoreError(t *testing.T) {
	t.Helper()
	scr := newScripted()
	scr.coreErr = true
	_, kind, err := SendLine(context.Background(), scr.session(), "sess-1", "core", "hi")
	if kind != ErrCore {
		t.Fatalf("kind = %q, want core", kind)
	}
	if err == nil || !strings.Contains(fmt.Sprintf("%v", err), "core said no") {
		t.Fatalf("error = %v, want core said no", err)
	}
}

func TestSendLineSkipsDecoyCorrelation(t *testing.T) {
	t.Helper()
	scr := newScripted()
	scr.doubleReply = true
	reply, kind, err := SendLine(context.Background(), scr.session(), "sess-1", "core", "hi")
	if err != nil {
		t.Fatalf("SendLine err: %v", err)
	}
	if kind != ErrNone {
		t.Fatalf("kind = %q, want none", kind)
	}
	if reply != "right" {
		t.Fatalf("reply = %q, want right (decoy must be skipped)", reply)
	}
}

// dialFrom adapts a sequence of scripted sessions into a Dialer, mirroring how
// the CLI re-dials a fresh, re-authenticated transport on each (re)connect.
// The Nth call returns the Nth session.
func dialFrom(sessions []*scripted) Dialer {
	var i int
	return func(ctx context.Context) (Session, error) {
		if i >= len(sessions) {
			return Session{}, errors.New("dialer exhausted: no scripted session left")
		}
		s := sessions[i]
		i++
		return s.session(), nil
	}
}

func TestRunConversationEndToEnd(t *testing.T) {
	t.Helper()
	ok := newScripted()
	sessions := []*scripted{ok}
	var out bytes.Buffer
	res, err := RunConversation(context.Background(), dialFrom(sessions), "core", strings.NewReader("hi\nthere\n"), &out)
	if err != nil {
		t.Fatalf("RunConversation: %v", err)
	}
	if res == nil {
		t.Fatal("no result")
	}
	if res.EndedBy != terminal.EndEOF {
		t.Fatalf("EndedBy = %q, want EOF", res.EndedBy)
	}
	if res.LinesIn != 2 {
		t.Fatalf("LinesIn = %d, want 2", res.LinesIn)
	}
	want := "echo: hi\necho: there\n"
	if out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
	// create + 2 interactions + close = 4 frames written; a single connection.
	_ = drainWritten(t, ok, 4)
	if ok.createCount != 1 {
		t.Fatalf("createCount = %d, want 1", ok.createCount)
	}
	if ok.selectCount != 0 {
		t.Fatalf("unexpected session.select (selectCount=%d)", ok.selectCount)
	}
}

func TestRunConversationCleanUpOnCancellation(t *testing.T) {
	t.Helper()
	ok := newScripted()
	sessions := []*scripted{ok}
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()

	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *terminal.Result, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := RunConversation(ctx, dialFrom(sessions), "core", r, &out)
		if err != nil {
			errs <- err
			return
		}
		done <- res
	}()

	// Deliver one line, then keep the pipe open (no EOF) and wait until the
	// conversation has sent session.create + interaction — i.e. it is now
	// blocked awaiting the next stdin line.
	if _, err := w.Write([]byte("hi\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = drainWritten(t, ok, 2)

	cancel()
	select {
	case res := <-done:
		if res.EndedBy != terminal.EndCancelled {
			t.Fatalf("EndedBy = %q, want cancelled", res.EndedBy)
		}
	case err := <-errs:
		t.Fatalf("error after cancel: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for cancellation")
	}
}

// M11.2: a mid-conversation transport drop must NOT end the conversation or
// panic (M11.1 regression). The next input re-dials, re-selects THE SAME
// session id, and resumes — the human continues the same conversation.
func TestRunConversationResumesSameSessionAcrossDrop(t *testing.T) {
	t.Helper()
	drop := newScripted()
	drop.transportErr = true
	ok := newScripted()
	sessions := []*scripted{drop, ok}
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()

	var out bytes.Buffer
	done := make(chan *terminal.Result, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := RunConversation(context.Background(), dialFrom(sessions), "core", r, &out)
		if err != nil {
			errs <- err
			return
		}
		done <- res
	}()

	if _, err := w.Write([]byte("one\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// dial #1 created the session and sent "one", which failed the transport:
	// the conversation is now disconnected, awaiting the next input.
	_ = drainWritten(t, drop, 2)

	if _, err := w.Write([]byte("two\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// EOF after the second line ends the (resumed) conversation cleanly.
	_ = w.Close()
	select {
	case res := <-done:
		if res.EndedBy != terminal.EndEOF {
			t.Fatalf("EndedBy = %q, want EOF after resume", res.EndedBy)
		}
		if res.LinesIn != 2 {
			t.Fatalf("LinesIn = %d, want 2", res.LinesIn)
		}
	case err := <-errs:
		t.Fatalf("unexpected error: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the resumed conversation")
	}

	// Exactly one session created; after the drop the SAME id was re-selected,
	// never a replacement created.
	if drop.createCount != 1 {
		t.Fatalf("drop.createCount = %d, want 1", drop.createCount)
	}
	if ok.createCount != 0 {
		t.Fatalf("a replacement session was created (ok.createCount=%d), want 0", ok.createCount)
	}
	if ok.selectCount != 1 {
		t.Fatalf("ok.selectCount = %d, want 1 (resume by select)", ok.selectCount)
	}
	if len(ok.selectedIDs) != 1 || ok.selectedIDs[0] != "sess-1" {
		t.Fatalf("selectedIDs = %v, want [sess-1]", ok.selectedIDs)
	}

	// Requirement 7: the interrupted "one" was NOT retransmitted after the
	// reconnect; only the distinct "two" was sent on the resumed session.
	if len(ok.interactionTexts) != 1 || ok.interactionTexts[0] != "two" {
		t.Fatalf("resumed session sent %v, want just [two]", ok.interactionTexts)
	}
	if !strings.Contains(out.String(), "echo: two") {
		t.Fatalf("output missing the resumed reply: %q", out.String())
	}
	if !strings.Contains(out.String(), "interrupted") {
		t.Fatalf("output missing the interruption notice: %q", out.String())
	}
}

// M11.2, requirement 6: if Core reports the session closed/revoked/unavailable
// on reconnect (session_unavailable), the conversation must fail explicitly —
// with a returned error, never a panic — and must NOT silently create a
// replacement session.
func TestRunConversationFailsClosedWhenSessionUnavailable(t *testing.T) {
	t.Helper()
	drop := newScripted()
	drop.transportErr = true
	refuse := newScripted()
	refuse.selectErr = true // session.select replies session_unavailable
	sessions := []*scripted{drop, refuse}
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()

	var out bytes.Buffer
	done := make(chan *terminal.Result, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := RunConversation(context.Background(), dialFrom(sessions), "core", r, &out)
		if err != nil {
			errs <- err
			return
		}
		done <- res
	}()

	if _, err := w.Write([]byte("one\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = drainWritten(t, drop, 2)

	if _, err := w.Write([]byte("two\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case err := <-errs:
		msg := fmt.Sprintf("%v", err)
		if !strings.Contains(msg, "cannot resume session") {
			t.Fatalf("error = %v, want an explicit cannot-resume error", err)
		}
		if !strings.Contains(msg, "session_unavailable") {
			t.Fatalf("error = %v, want session_unavailable", err)
		}
	case res := <-done:
		t.Fatalf("conversation ended cleanly (EndedBy=%q) despite an unavailable session", res.EndedBy)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the fail-closed error")
	}

	// Never silently created a replacement on the refused dial.
	if drop.createCount != 1 {
		t.Fatalf("drop.createCount = %d, want 1", drop.createCount)
	}
	if refuse.createCount != 0 {
		t.Fatalf("replacement created on refused dial (createCount=%d), want 0", refuse.createCount)
	}
	if refuse.selectCount != 1 {
		t.Fatalf("refuse.selectCount = %d, want 1", refuse.selectCount)
	}
}

func TestSelectSessionResumesActiveSession(t *testing.T) {
	t.Helper()
	scr := newScripted()
	sid, err := SelectSession(context.Background(), scr.session(), "sc-x", "sess-1")
	if err != nil {
		t.Fatalf("SelectSession: %v", err)
	}
	if sid != "sess-1" {
		t.Fatalf("sid = %q, want sess-1", sid)
	}
	_ = drainWritten(t, scr, 1)
	if scr.selectCount != 1 || len(scr.selectedIDs) != 1 || scr.selectedIDs[0] != "sess-1" {
		t.Fatalf("selectCount=%d selectedIDs=%v, want 1 x sess-1", scr.selectCount, scr.selectedIDs)
	}
}

// M11.2, requirement 6 nuance: even a Status=ok select must be rejected if the
// session is not reported active (e.g. closed). Never proceed on a session that
// Core does not confirm as active.
func TestSelectSessionRejectsNonActiveStatus(t *testing.T) {
	t.Helper()
	scr := newScripted()
	scr.selectStatus = "closed" // Core reports an existing-but-closed session
	_, err := SelectSession(context.Background(), scr.session(), "sc-x", "sess-1")
	if err == nil {
		t.Fatal("SelectSession succeeded for a closed session")
	}
	if !strings.Contains(fmt.Sprintf("%v", err), "not active") {
		t.Fatalf("error = %v, want an explicit not-active rejection", err)
	}
}

// Regression: awaitReply must require correlation_id == msgID, so an
// unsolicited event with an absent correlation id is ignored rather than
// mistaken for the Doll's reply.
func TestSendLineIgnoresUncorrelatedEvent(t *testing.T) {
	t.Helper()
	scr := newScripted()
	scr.unsolicited = true
	reply, kind, err := SendLine(context.Background(), scr.session(), "sess-1", "core", "hi")
	if err != nil {
		t.Fatalf("SendLine err: %v", err)
	}
	if kind != ErrNone {
		t.Fatalf("kind = %q, want none", kind)
	}
	if reply != "echo: hi" {
		t.Fatalf("reply = %q, want %q (unsolicited empty-correlation event must be ignored)", reply, "echo: hi")
	}
}
