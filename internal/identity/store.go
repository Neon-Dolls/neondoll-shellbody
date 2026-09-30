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
// atomic: we open the identity file with O_CREATE|O_EXCL, so exactly one
// goroutine can win the race to create the file.
func (s *Store) CreateFresh(name string, meta BodyMetadata) (*IdentityState, error) {
	if err := s.EnsureDir(); err != nil {
		return nil, err
	}
	st, err := NewIdentityState(name, meta)
	if err != nil {
		return nil, err
	}
	raw, err := st.Marshal()
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(s.identityPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, identityMode)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrIdentityExists
		}
		return nil, fmt.Errorf("identity: open %s: %w", s.identityPath(), err)
	}
	defer func() {
		// If we succeed, we will keep the file. On any error before success,
		// we remove it to avoid leaving a partial file.
		if err != nil {
			_ = os.Remove(s.identityPath())
		}
	}()

	if _, err := f.Write([]byte(raw)); err != nil {
		return nil, err
	}
	if err := f.Sync(); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return st, nil
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
