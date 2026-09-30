package terminal

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func runEcho(t *testing.T, input string) (output string, res *Result, err error) {
	t.Helper()
	var out bytes.Buffer
	tm := New(strings.NewReader(input), &out, nil)
	res, err = tm.Run(context.Background())
	return out.String(), res, err
}

func TestEchoLines(t *testing.T) {
	got, res, err := runEcho(t, "hello\nworld\n")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LinesIn != 2 {
		t.Fatalf("LinesIn = %d, want 2", res.LinesIn)
	}
	if res.EndedBy != EndEOF {
		t.Fatalf("EndedBy = %q, want %q", res.EndedBy, EndEOF)
	}
	want := "[1] hello\n[2] world\n"
	if got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestEofWithNoTerminalNewline(t *testing.T) {
	got, res, err := runEcho(t, "hello") // no trailing newline
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LinesIn != 1 {
		t.Fatalf("LinesIn = %d, want 1", res.LinesIn)
	}
	if res.EndedBy != EndEOF {
		t.Fatalf("EndedBy = %q, want EOF", res.EndedBy)
	}
	if got != "[1] hello\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestEmptyInputEndsImmediately(t *testing.T) {
	_, res, err := runEcho(t, "")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LinesIn != 0 || res.EndedBy != EndEOF {
		t.Fatalf("res = %+v, want clean immediate EOF", res)
	}
}

func TestCarriageReturnStripped(t *testing.T) {
	got, _, err := runEcho(t, "hello\r\n")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "[1] hello\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestCustomReflex(t *testing.T) {
	fn := func(line string, n int) string {
		return strings.ToUpper(line)
	}
	var out bytes.Buffer
	tm := New(strings.NewReader("abc\n"), &out, fn)
	res, err := tm.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.LinesIn != 1 {
		t.Fatalf("LinesIn = %d", res.LinesIn)
	}
	if out.String() != "ABC\n" {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCancellationStopsMidStream(t *testing.T) {
	// Input that supplies one line then blocks (no EOF). Cancellation must
	// end the run cleanly even while a read is pending.
	r, w := io.Pipe()
	defer r.Close()

	// A writer that signals us the moment the first line's reflex output is
	// produced, so the LinesIn assertion is deterministic (no racy polling
	// of a shared buffer).
	firstDone := make(chan struct{})
	out := &signalWriter{onWrite: func(s string) {
		if s == "[1] first\n" {
			close(firstDone)
		}
	}}
	tm := New(r, out, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *Result, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := tm.Run(ctx)
		if err != nil {
			errs <- err
			return
		}
		done <- res
	}()

	// Deliver one line, then hold the pipe open (no EOF) until we cancel.
	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Wait until the first line has actually been processed and echoed.
	select {
	case <-firstDone:
	case err := <-errs:
		t.Fatalf("unexpected error: %v", err)
	case res := <-done:
		t.Fatalf("run ended before cancellation: %+v", res)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first line to be processed")
	}

	// Now cancel: Run must return promptly.
	cancel()
	select {
	case res := <-done:
		if res.EndedBy != EndCancelled {
			t.Fatalf("EndedBy = %q, want cancelled", res.EndedBy)
		}
		if res.LinesIn != 1 {
			t.Fatalf("LinesIn = %d, want 1", res.LinesIn)
		}
	case err := <-errs:
		t.Fatalf("error after cancel: %v", err)
	}
}

// signalWriter forwards writes and invokes onWrite for each write.
type signalWriter struct {
	onWrite func(string)
}

func (s *signalWriter) Write(p []byte) (int, error) {
	if s.onWrite != nil {
		s.onWrite(string(p))
	}
	return len(p), nil
}
