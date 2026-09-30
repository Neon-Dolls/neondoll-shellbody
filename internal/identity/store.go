package identity

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// States directory modes. The identity file is world-unreadable because it
// is part of durable Body state; secrets in later milestones live alongside
// it.
const (
	dirMode           = 0o700
	identityMode      = 0o600
	tempDirPrefix     = ".body-tmp-"
	identityTmpSuffix = ".tmp-write"
)

// Store persists Body identity under an explicit, configurable state
// directory. It mirrors the reference Body's create-once, atomic-commit
// store semantics but carries identity only (no WireGuard keys, no
// membership) until later milestones.
type Store struct {
	dir string
}

// NewStore returns a Store rooted at dir. The directory need not exist yet.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("identity: state directory is empty")
	}
	return &Store{dir: dir}, nil
}

// Dir returns the state directory this Store is rooted at.
func (s *Store) Dir() string { return s.dir }

// EnsureDir creates the state directory (with its parents) if missing.
func (s *Store) EnsureDir() error {
	if err := os.MkdirAll(s.dir, dirMode); err != nil {
		return fmt.Errorf("identity: create state dir %s: %w", s.dir, err)
	}
	return nil
}

// identityPath is the full path of the identity file within the state dir.
func (s *Store) identityPath() string { return filepath.Join(s.dir, identityFileName) }

// HasIdentity reports whether a durable identity already exists.
func (s *Store) HasIdentity() bool {
	st, err := os.Stat(s.identityPath())
	return err == nil && !st.IsDir()
}

// LoadIdentity reads and validates the persisted identity. It returns
// ErrStateNotFound when no identity has been committed yet.
func (s *Store) LoadIdentity() (*IdentityState, error) {
	raw, err := os.ReadFile(s.identityPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrStateNotFound
		}
		return nil, fmt.Errorf("identity: read %s: %w", s.identityPath(), err)
	}
	st, err := UnmarshalIdentity(string(raw))
	if err != nil {
		return nil, fmt.Errorf("identity: load: %w", err)
	}
	return st, nil
}

// CreateFresh creates a new identity and durably commits it. It fails with
// ErrIdentityExists if a durable identity already exists: identity creation
// is create-once and never silently replaces an existing Body. The commit is
// atomic: a temp file in the same directory is fsynced and renamed over the
// target, so a crash never leaves a half-written identity file.
func (s *Store) CreateFresh(name string, meta BodyMetadata) (*IdentityState, error) {
	if err := s.EnsureDir(); err != nil {
		return nil, err
	}
	if s.HasIdentity() {
		return nil, ErrIdentityExists
	}

	st, err := NewIdentityState(name, meta)
	if err != nil {
		return nil, err
	}
	raw, err := st.Marshal()
	if err != nil {
		return nil, err
	}

	if err := writeIdentityAtomic(s.identityPath(), []byte(raw)); err != nil {
		return nil, err
	}
	return st, nil
}

// writeIdentityAtomic writes data to path via a same-directory temp file and
// an atomic rename, fsyncing both file and directory. It removes the temp
// file on any error.
func writeIdentityAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, identityFileName+identityTmpSuffix)
	if err != nil {
		return fmt.Errorf("identity: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // best-effort; harmless if already gone
	}()

	if modeErr := os.Chmod(tmpName, identityMode); modeErr != nil {
		return fmt.Errorf("identity: chmod temp: %w", modeErr)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("identity: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("identity: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("identity: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("identity: commit rename: %w", err)
	}
	// Best-effort directory fsync for durable rename.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// StateError wraps store failures with the offending operation.
type StateError struct {
	Op  string
	Err error
}

func (e *StateError) Error() string {
	if e.Err == nil {
		return "identity: " + e.Op
	}
	return fmt.Sprintf("identity: %s: %v", e.Op, e.Err)
}

func (e *StateError) Unwrap() error { return e.Err }

// ErrStateNotFound is returned when no durable identity exists yet.
var ErrStateNotFound = &StateError{Op: "state_not_found"}

// ErrIdentityExists is returned when an identity already exists and creation
// is attempted again.
var ErrIdentityExists = &StateError{Op: "identity_exists"}
