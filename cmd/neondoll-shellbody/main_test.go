package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// bodyIDRe matches the body_id line emitted by --init/--status/banner.
var bodyIDRe = regexp.MustCompile(`body_id:\s+(body_[0-9a-f]+)`)

// buildBin compiles the shellbody binary once and returns its path.
func buildBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "neondoll-shellbody")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// M1 acceptance proof:
//  1. initialize a fresh state directory and record the Body ID
//  2. exit
//  3. restart against the same state directory and observe the same Body ID
//  4. a separate state directory yields a distinct Body
//  5. terminal runtime exits cleanly on EOF without corrupting state
func TestM1AcceptanceProof(t *testing.T) {
	bin := buildBin(t)

	stateA := filepath.Join(t.TempDir(), "state-a")
	stateB := filepath.Join(t.TempDir(), "state-b")

	runBin := func(args ...string) (stdout, stderr string, err error) {
		var ob, eb bytes.Buffer
		c := exec.Command(bin, args...)
		c.Stdout = &ob
		c.Stderr = &eb
		err = c.Run()
		return ob.String(), eb.String(), err
	}

	// Step 1: initialize a fresh state directory.
	stdout, _, err := runBin("--init", "--state-dir", stateA)
	if err != nil {
		t.Fatalf("init failed: %v", err)
	}
	m := bodyIDRe.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("init output missing body_id: %q", stdout)
	}
	firstID := m[1]

	// Step 2: (exit) — Step 3: restart against the same state dir -> same ID.
	stdout, _, err = runBin("--status", "--state-dir", stateA)
	if err != nil {
		t.Fatalf("status after restart failed: %v", err)
	}
	m = bodyIDRe.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("status output missing body_id: %q", stdout)
	}
	if m[1] != firstID {
		t.Fatalf("restart body id = %q, want stable %q", m[1], firstID)
	}

	// Step 4: a separate state directory -> distinct Body.
	stdout, _, err = runBin("--init", "--state-dir", stateB)
	if err != nil {
		t.Fatalf("init state-b failed: %v", err)
	}
	m = bodyIDRe.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("init state-b missing body_id: %q", stdout)
	}
	if m[1] == firstID {
		t.Fatalf("distinct state dir produced same body id %q", m[1])
	}

	// Step 5: interactive run ends cleanly on EOF without corrupting state.
	cmd := exec.Command(bin, "--state-dir", stateA)
	cmd.Stdin = strings.NewReader("hello\n") // then EOF
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	if err := cmd.Run(); err != nil {
		t.Fatalf("interactive run: %v\nstderr: %s", err, se.String())
	}
	if !strings.Contains(se.String(), "ended (eof") {
		t.Fatalf("expected clean EOF notice in stderr, got %q", se.String())
	}
	if !strings.Contains(so.String(), "[1] hello") {
		t.Fatalf("expected echoed line in stdout, got %q", so.String())
	}

	// State must still be valid and stable after the run.
	stdout, _, err = runBin("--status", "--state-dir", stateA)
	if err != nil {
		t.Fatalf("status after interactive run: %v", err)
	}
	m = bodyIDRe.FindStringSubmatch(stdout)
	if m == nil || m[1] != firstID {
		t.Fatalf("body id after run = %q, want %q (state corrupted?)", m[1], firstID)
	}
}

// TestInteractiveDoesNotRecreateIdentity: starting interactively on a fresh
// dir auto-creates identity; on a second run it must reuse it (restart
// stability without --init).
func TestInteractiveDoesNotRecreateIdentity(t *testing.T) {
	bin := buildBin(t)
	stateDir := filepath.Join(t.TempDir(), "state")

	run := func() string {
		cmd := exec.Command(bin, "--state-dir", stateDir)
		cmd.Stdin = strings.NewReader("\n")
		var so, se bytes.Buffer
		cmd.Stdout = &so
		cmd.Stderr = &se
		if err := cmd.Run(); err != nil {
			t.Fatalf("run: %v\n%s", err, se.String())
		}
		return so.String()
	}

	first := run()
	m1 := bodyIDRe.FindStringSubmatch(first)
	if m1 == nil {
		t.Fatalf("first run banner missing body_id: %q", first)
	}
	second := run()
	m2 := bodyIDRe.FindStringSubmatch(second)
	if m2 == nil {
		t.Fatalf("second run banner missing body_id: %q", second)
	}
	if m1[1] != m2[1] {
		t.Fatalf("interactive restart changed body id: %q -> %q", m1[1], m2[1])
	}
}

// TestNoIdentityStatusOnFreshDir: --status on a fresh dir reports none.
func TestNoIdentityStatusOnFreshDir(t *testing.T) {
	bin := buildBin(t)
	dir := filepath.Join(t.TempDir(), "empty")
	cmd := exec.Command(bin, "--status", "--state-dir", dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected error on --status of fresh dir, got none: %s", out)
	}
	if !bytes.Contains(out, []byte("no identity yet")) {
		t.Fatalf("unexpected error text: %s", out)
	}
}

// TestInitOnExistingIdentityFails: duplicating identity is forbidden.
func TestInitOnExistingIdentityFails(t *testing.T) {
	bin := buildBin(t)
	dir := filepath.Join(t.TempDir(), "state")
	if out, err := exec.Command(bin, "--init", "--state-dir", dir).CombinedOutput(); err != nil {
		t.Fatalf("first init failed: %v\n%s", err, out)
	}
	out, err := exec.Command(bin, "--init", "--state-dir", dir).CombinedOutput()
	if err == nil {
		t.Fatal("second --init should fail")
	}
	if !bytes.Contains(out, []byte("identity_exists")) && !bytes.Contains(out, []byte("already exists")) {
		t.Fatalf("unexpected error text: %s", out)
	}
}

// TestSigintShutdown: interactive run must exit cleanly (code 0) when the
// process receives SIGINT (Ctrl-C) while idle on a blocked read, and must not
// corrupt the persisted identity. Standard-library only; sends os.Interrupt
// to the child process.
func TestSigintShutdown(t *testing.T) {
	if testing.Short() {
		t.Skip("SIGINT subprocess test skipped in short mode")
	}
	bin := buildBin(t)
	dir := filepath.Join(t.TempDir(), "state")
	if out, err := exec.Command(bin, "--init", "--state-dir", dir).CombinedOutput(); err != nil {
		t.Fatalf("init failed: %v\n%s", err, out)
	}

	cmd := exec.Command(bin, "--state-dir", dir)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Let it come up and block on the read, then send Ctrl-C.
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	select {
	case <-time.After(500 * time.Millisecond):
		if err := cmd.Process.Signal(os.Interrupt); err != nil {
			t.Fatalf("send SIGINT: %v", err)
		}
		stdin.Close() // release in case signal handler needs the read to return
	case err := <-done:
		t.Fatalf("process exited early: %v\n%s", err, errBuf.String())
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("expected clean exit after SIGINT, got error: %v\nstderr: %s", err, errBuf.String())
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("process did not exit within 3s of SIGINT (stderr: %s)", errBuf.String())
	}

	// State must be intact and stable.
	stdout, err := exec.Command(bin, "--status", "--state-dir", dir).CombinedOutput()
	if err != nil {
		t.Fatalf("status after SIGINT: %v\n%s", err, stdout)
	}
	if !bytes.Contains(stdout, []byte("body_id:")) {
		t.Fatalf("status after SIGINT missing body_id: %s", stdout)
	}
}
