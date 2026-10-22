package enrollment

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadRecordCorruptStateFailsClosed (regression): a corrupt/unreadable
// enrollment state file must surface as a real, non-"not found" error — never
// as ErrStateNotFound. cmd/neondoll-shellbody --enroll depends on this to fail
// closed instead of silently discarding an unverifiable trust anchor and
// re-enrolling a Body that may already be pinned to a Core.
func TestLoadRecordCorruptStateFailsClosed(t *testing.T) {
	store := NewStore(t.TempDir() + "/es")
	_ = os.MkdirAll(filepath.Dir(store.path), 0o700)
	f, err := os.OpenFile(store.path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open corrupt state file: %v", err)
	}
	if _, err := f.Write([]byte("this is not valid enrollment json {{corrupt")); err != nil {
		_ = f.Close()
		t.Fatalf("write corrupt state file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close corrupt state file: %v", err)
	}

	_, lerr := store.LoadRecord()
	if lerr == nil {
		t.Fatal("expected an error for a corrupt state file")
	}
	if errors.Is(lerr, ErrStateNotFound) {
		t.Fatalf("corrupt state file must not be treated as no enrollment: %v", lerr)
	}
}
