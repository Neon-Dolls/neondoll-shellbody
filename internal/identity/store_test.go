package identity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return st
}

func TestCreateFreshThenLoad(t *testing.T) {
	st := newTestStore(t)
	created, err := st.CreateFresh("Alice", testMeta())
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	if !st.HasIdentity() {
		t.Fatal("HasIdentity() = false after CreateFresh")
	}

	loaded, err := st.LoadIdentity()
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	if loaded.BodyID() != created.BodyID() {
		t.Fatalf("loaded id = %q, want %q", loaded.BodyID(), created.BodyID())
	}
}

func TestRestartStabilitySameDirectory(t *testing.T) {
	// Simulate process restart against the same state directory.
	dir := t.TempDir()
	st, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	first, err := st.CreateFresh("Alice", testMeta())
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}

	st2, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore reload: %v", err)
	}
	second, err := st2.LoadIdentity()
	if err != nil {
		t.Fatalf("LoadIdentity after restart: %v", err)
	}
	if second.BodyID() != first.BodyID() {
		t.Fatalf("restart id = %q, want stable %q", second.BodyID(), first.BodyID())
	}
}

func TestDistinctDirectoriesGiveDistinctBodies(t *testing.T) {
	a := newTestStore(t)
	b := newTestStore(t)

	ida, err := a.CreateFresh("A", testMeta())
	if err != nil {
		t.Fatalf("CreateFresh A: %v", err)
	}
	idb, err := b.CreateFresh("B", testMeta())
	if err != nil {
		t.Fatalf("CreateFresh B: %v", err)
	}
	if ida.BodyID() == idb.BodyID() {
		t.Fatalf("two state dirs produced same body id %q", ida.BodyID())
	}
}

func TestCreateFreshWhenIdentityExistsFails(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.CreateFresh("Alice", testMeta()); err != nil {
		t.Fatalf("first CreateFresh: %v", err)
	}
	if _, err := st.CreateFresh("Bob", testMeta()); err == nil {
		t.Fatal("second CreateFresh should fail with ErrIdentityExists")
	} else if !errors.Is(err, ErrIdentityExists) {
		t.Fatalf("want ErrIdentityExists, got %v", err)
	}
}

func TestLoadIdentityOnFreshDirReturnsStateNotFound(t *testing.T) {
	st := newTestStore(t)
	_, err := st.LoadIdentity()
	if !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("want ErrStateNotFound, got %v", err)
	}
}

func TestEnsureDirCreatesParents(t *testing.T) {
	base := t.TempDir()
	deep := filepath.Join(base, "a", "b", "c")
	st, err := NewStore(deep)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := st.EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	info, err := os.Stat(deep)
	if err != nil {
		t.Fatalf("stat deep dir: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("deep path is not a directory")
	}
}

func TestCreateFreshWritesRestrictiveMode(t *testing.T) {
	st := newTestStore(t)
	if _, err := st.CreateFresh("Alice", testMeta()); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	info, err := os.Stat(st.identityPath())
	if err != nil {
		t.Fatalf("stat identity file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != identityMode {
		t.Fatalf("identity file mode = %o, want %o", perm, identityMode)
	}
}

func TestWriteIdentityAtomicLeftNeitherGarbageNorDuplicates(t *testing.T) {
	// A successful CreateFresh leaves exactly one identity file (no temp
	// leftovers), and the file parses cleanly.
	st := newTestStore(t)
	if _, err := st.CreateFresh("Alice", testMeta()); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	entries, err := os.ReadDir(st.Dir())
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("state dir has %d entries, want 1 (no temp files left): %v", len(entries), entries)
	}
	if _, err := st.LoadIdentity(); err != nil {
		t.Fatalf("identity file does not parse: %v", err)
	}
}
