// Package body — durable Shell Body state.
//
// M2 persistence semantics:
//   - a fresh installation creates the Body identity and WG keypair exactly
//     once (create-once, enforced atomically);
//   - a restart loads the same Body identity and WG keypair;
//   - membership is written only after a successful pairing has validated the
//     complete Core response; a failed/denied/malformed attempt never leaves
//     an apparently valid membership;
//   - missing or corrupt state fails safely — it is NEVER silently replaced;
//   - identity + private WG key are written to owner-only files.
//
// The store preserves the Shell Body's create-once guarantee (from M1: two
// simultaneous first starts cannot both succeed) and extends it to persist the
// WG keypair and network membership.
package body

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/identity"
)

// stateFileName is the on-disk name of the durable identity file.
const stateFileName = "body.json"

// wgPrivateFileName is the on-disk name of the WG private key.
const wgPrivateFileName = "wg_private.key"

// membershipFileName is the on-disk name of the durable network membership
// file, written only after a successful pairing commits the membership.
const membershipFileName = "membership.json"

// Store persists Body state on disk under a state directory.
type Store struct {
	dir string
}

// StateError describes a local store failure (missing, corrupt, io).
type StateError struct {
	Op  string
	Err error
}

func (e *StateError) Error() string {
	if e.Err != nil {
		return "body: " + e.Op + ": " + e.Err.Error()
	}
	return "body: " + e.Op
}

// ErrStateNotFound is returned when no identity exists yet (fresh install).
var ErrStateNotFound error = &StateError{Op: "not_found"}

// ErrIdentityExists is returned when a fresh create is attempted but an
// identity already exists. The Body identity is create-once: a second create
// must never destroy the existing identity.
var ErrIdentityExists error = &StateError{Op: "identity_exists"}

// NewStore returns a store rooted at the given state directory. The directory
// need not exist yet.
func NewStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, &StateError{Op: "empty_dir", Err: errors.New("state directory is empty")}
	}
	return &Store{dir: dir}, nil
}

// Dir returns the state directory this store is rooted at.
func (s *Store) Dir() string { return s.dir }

// EnsureDir creates the state directory (and parents) if missing.
func (s *Store) EnsureDir() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return &StateError{Op: "mkdir", Err: err}
	}
	return nil
}

// statePath returns the path to the identity state file.
func (s *Store) statePath() string { return filepath.Join(s.dir, stateFileName) }

// wgPrivatePath returns the path to the WG private key file.
func (s *Store) wgPrivatePath() string { return filepath.Join(s.dir, wgPrivateFileName) }

// membershipPath returns the path to the durable membership file.
func (s *Store) membershipPath() string { return filepath.Join(s.dir, membershipFileName) }

// HasIdentity reports whether an identity file exists on disk.
func (s *Store) HasIdentity() bool {
	_, err := os.Stat(s.statePath())
	return err == nil
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so the destination is never observed partially
// written (readers see old or new full content, never a truncated middle).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".write-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// LoadIdentity reads the persisted identity. ErrStateNotFound if none exists;
// StateError if the file is corrupt or the wrong version. It never
// regenerates implicitly.
func (s *Store) LoadIdentity() (*identity.IdentityState, error) {
	raw, err := os.ReadFile(s.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrStateNotFound
		}
		return nil, &StateError{Op: "read identity", Err: err}
	}
	st, err := identity.UnmarshalIdentity(string(raw))
	if err != nil {
		return nil, &StateError{Op: "parse identity", Err: err}
	}
	return st, nil
}

// LoadWgKeypair reads the persisted WG private key and recomputes the public
// key. ErrStateNotFound if the private key file is missing.
func (s *Store) LoadWgKeypair() (*WgKeypair, error) {
	raw, err := os.ReadFile(s.wgPrivatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrStateNotFound
		}
		return nil, &StateError{Op: "read wg key", Err: err}
	}
	priv, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return nil, &StateError{Op: "decode wg key", Err: err}
	}
	defer Wipe(priv)
	kp, err := NewWgKeypair(priv)
	if err != nil {
		return nil, &StateError{Op: "reconstruct wg key", Err: err}
	}
	return kp, nil
}

// SaveWgKeypair persists the WG private key atomically with owner-only
// permissions. The public key is recomputed and not stored separately.
func (s *Store) SaveWgKeypair(kp *WgKeypair) error {
	if kp == nil {
		return &StateError{Op: "save wg key", Err: errors.New("empty keypair")}
	}
	if err := s.EnsureDir(); err != nil {
		return err
	}
	priv := kp.PrivateKeyBytes()
	defer Wipe(priv)
	enc := base64.StdEncoding.EncodeToString(priv)
	if err := writeFileAtomic(s.wgPrivatePath(), []byte(enc), 0o600); err != nil {
		return &StateError{Op: "write wg key", Err: err}
	}
	return nil
}

// LoadMembership reads the persisted network membership. ErrStateNotFound if
// no membership has been committed yet (pre-pairing); StateError if the file
// is corrupt. It never injects a default membership.
func (s *Store) LoadMembership() (*Membership, error) {
	raw, err := os.ReadFile(s.membershipPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrStateNotFound
		}
		return nil, &StateError{Op: "read membership", Err: err}
	}
	m, err := UnmarshalMembership(string(raw))
	if err != nil {
		return nil, &StateError{Op: "parse membership", Err: err}
	}
	return m, nil
}

// SaveMembership writes the network membership durably and atomically after a
// successful pairing. It is only called once the entire response has validated
// and the membership is ready to commit coherently. The invitation secret is
// never embedded in the membership file.
func (s *Store) SaveMembership(m *Membership) error {
	if m == nil {
		return &StateError{Op: "save membership", Err: errors.New("empty membership")}
	}
	if err := s.EnsureDir(); err != nil {
		return err
	}
	enc, err := MarshalMembership(m)
	if err != nil {
		return &StateError{Op: "marshal membership", Err: err}
	}
	if err := writeFileAtomic(s.membershipPath(), []byte(enc), 0o600); err != nil {
		return &StateError{Op: "write membership", Err: err}
	}
	return nil
}

// LoadOrError returns (identity, wgkey) or ErrStateNotFound when nothing
// exists yet. It fails closed if one half of the Body state is missing or
// corrupt: an identity present without its key, or a key present without an
// identity, is never silently repaired.
func (s *Store) LoadOrError() (*identity.IdentityState, *WgKeypair, error) {
	st, idErr := s.LoadIdentity()
	if idErr != nil {
		if idErr == ErrStateNotFound {
			return nil, nil, ErrStateNotFound
		}
		return nil, nil, idErr
	}
	kp, kpErr := s.LoadWgKeypair()
	if kpErr != nil {
		if kpErr == ErrStateNotFound {
			return nil, nil, &StateError{Op: "identity present but wg key missing", Err: nil}
		}
		return nil, nil, kpErr
	}
	return st, kp, nil
}

// FreshResult is the outcome of an explicit fresh-state creation.
type FreshResult struct {
	State *identity.IdentityState
	Key   *WgKeypair
}

// CreateFresh creates and persists a brand-new identity and WG keypair.
//
// Fresh initialization is atomic and create-once:
//   - the identity file (body.json) is claimed first with an exclusive create
//     (O_CREATE|O_EXCL), closing the check-then-act race so concurrent
//     CreateFresh calls cannot both succeed;
//   - the WG key is written before identity content is committed, so a
//     fully-written identity never appears without its key;
//   - if any step fails, the claimed files are rolled back so a failed init
//     never leaves a half-created Body (body.json without wg_private.key).
//
// If an identity already exists, CreateFresh returns ErrIdentityExists and
// does NOT overwrite it. Callers that need a truly fresh volume must point the
// store at a new, empty directory.
func (s *Store) CreateFresh(name string, meta identity.BodyMetadata) (*FreshResult, error) {
	// Build the full in-memory state before any disk write.
	st, err := identity.NewIdentityState(name, meta)
	if err != nil {
		return nil, err
	}
	kp, err := GenerateWgKeypair()
	if err != nil {
		return nil, err
	}
	data, err := st.Marshal()
	if err != nil {
		return nil, &StateError{Op: "marshal identity", Err: err}
	}

	if err := s.EnsureDir(); err != nil {
		return nil, err
	}

	// Atomic create-once gate.
	f, err := os.OpenFile(s.statePath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, ErrIdentityExists
		}
		return nil, &StateError{Op: "create identity", Err: err}
	}

	// We hold the exclusive claim. Any failure below must roll back the
	// claimed identity (and any key we wrote) so no half-created Body persists.
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = f.Close()
		_ = os.Remove(s.statePath())
		_ = os.Remove(s.wgPrivatePath())
	}()

	// Persist the WG key first; writeFileAtomic leaves nothing behind on
	// failure, and if it fails the deferred rollback removes body.json too.
	if err := s.SaveWgKeypair(kp); err != nil {
		return nil, err
	}

	// Write identity content into the claimed file, then commit.
	if _, err := f.Write([]byte(data)); err != nil {
		return nil, &StateError{Op: "write identity", Err: err}
	}
	if err := f.Chmod(0o600); err != nil {
		return nil, &StateError{Op: "chmod identity", Err: err}
	}
	if err := f.Sync(); err != nil {
		return nil, &StateError{Op: "sync identity", Err: err}
	}
	if err := f.Close(); err != nil {
		return nil, &StateError{Op: "close identity", Err: err}
	}

	committed = true
	return &FreshResult{State: st, Key: kp}, nil
}
