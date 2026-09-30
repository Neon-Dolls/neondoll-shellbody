// Package terminal provides the terminal embodiment of the Shell Body,
// cleanly separated from main.
//
// A terminal is a line-oriented input/output boundary: it reads input and
// produces output. For the M1 milestone the Shell Body performs no Doll
// cognition, so the terminal applies a deterministic Reflex to each line
// (a stateless echo) and must never fabricate a Doll response. The
// interactive loop drives input until clean EOF or context cancellation
// (Ctrl-C / SIGINT), then shuts down without corrupting durable Body state.
package terminal

import (
	"bufio"
	"context"
	"fmt"
	"io"
)

// Reflex transforms one input line into an output line deterministically.
// It is the seam where later milestones (Doll Link sessions, capability
// execution) will plug real behavior in. M1 ships a deterministic echo.
type Reflex func(line string) string

// Echo is the deterministic M1 reflex: it reports the line back with its
// sequence number. It is purely mechanical and never impersonates a Doll.
func Echo(line string, n int) string {
	return fmt.Sprintf("[%d] %s", n, line)
}

// Terminal is a line-oriented input/output boundary.
type Terminal struct {
	in  *bufio.Reader
	out io.Writer
	fn  func(line string, n int) string
}

// New returns a Terminal reading from r and writing to w, applying fn to
// each line. fn defaults to Echo when nil.
func New(r io.Reader, w io.Writer, fn func(line string, n int) string) *Terminal {
	if fn == nil {
		fn = Echo
	}
	return &Terminal{in: bufio.NewReader(r), out: w, fn: fn}
}

// Result describes a completed interactive run.
type Result struct {
	// LinesIn is how many lines were read from input.
	LinesIn int
	// EndedBy describes what terminated the run.
	EndedBy string
}

// End reasons.
const (
	EndEOF       = "eof"
	EndCancelled = "cancelled"
)

// Run drives the interactive loop until clean EOF or ctx cancellation
// (Ctrl-C / SIGINT). It reads each line, applies the reflex, and writes the
// result. On EOF it returns EndEOF; on context cancellation EndCancelled.
// Durable Body state is never touched here, so a run cannot corrupt it.
func (t *Terminal) Run(ctx context.Context) (*Result, error) {
	// Lines are read in a goroutine so that a blocked read on stdin does not
	// prevent clean shutdown on Ctrl-C. A size-1 channel plus the ctx select
	// means cancellation returns even while a read is pending.
	lines := make(chan lineResult, 1)

	go t.readLoop(lines)

	res := &Result{}
	for {
		select {
		case <-ctx.Done():
			res.EndedBy = EndCancelled
			return res, nil
		case lr := <-lines:
			switch {
			case lr.err == io.EOF:
				res.EndedBy = EndEOF
				return res, nil
			case lr.err != nil:
				return res, lr.err
			}
			res.LinesIn++
			reply := t.fn(lr.line, res.LinesIn)
			if _, err := fmt.Fprintln(t.out, reply); err != nil {
				return res, err
			}
		}
	}
}

// lineResult carries one read outcome.
type lineResult struct {
	line string
	err  error
}

// readLoop reads lines and sends them to ch until EOF or error.
func (t *Terminal) readLoop(ch chan<- lineResult) {
	for {
		line, err := t.in.ReadString('\n')
		if err != nil {
			// Deliver a final partial line (e.g. "hello" without trailing
			// newline) before signalling EOF. The partial has no trailing
			// newline, so it must not go through trimNewline.
			if len(line) > 0 && err == io.EOF {
				ch <- lineResult{line: stripCR(line)}
			}
			ch <- lineResult{err: err}
			return
		}
		ch <- lineResult{line: trimNewline(line)}
	}
}

func trimNewline(s string) string {
	s = s[:len(s)-1]
	return stripCR(s)
}

// stripCR removes a trailing carriage return, used for CRLF input.
func stripCR(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\r' {
		s = s[:len(s)-1]
	}
	return s
}
