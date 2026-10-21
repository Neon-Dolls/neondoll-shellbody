package enrollment

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ErrStateNotFound is returned when the enrollment state file (or a requested
// component of it) does not exist yet.
var ErrStateNotFound = errors.New("enrollment: state not found")

// Record is the durable outcome of a successful enrollment with Core. Core
// mints the BodyID at completion; the Body stores what Core issued.
type Record struct {
	CoreID                   string            `json:"core_id"`
	CorePublicKey            string            `json:"core_public_key,omitempty"` // Core's Ed25519 public key (base64url), trust anchor for auth
	BodyID                   string            `json:"body_id"`
	BodyPublicKeyFingerprint string            `json:"body_key_fingerprint"`
	MembershipReference      string            `json:"membership_reference"`
	Permissions              []string          `json:"permissions,omitempty"`
	Receipt                  EnrollmentReceipt `json:"receipt"`
	EnrolledAt               time.Time         `json:"enrolled_at"`
}

// stateFile is the on-disk enrollment state (identity seed + record).
type stateFile struct {
	Version int     `json:"version"`
	SeedB64 string  `json:"identity_seed_b64"`
	Record  *Record `json:"record,omitempty"`
}

// Store persists the Body's enrollment identity and the Core-issued record.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore returns a Store persisting to dir from a state file.
func NewStore(dir string) *Store {
	return &Store{path: filepath.Join(dir, "enrollment.json")}
}

// HasIdentity reports whether a durable identity exists.
func (s *Store) HasIdentity() bool {
	_, err := s.LoadIdentity()
	return err == nil
}

// LoadIdentity loads the persisted Ed25519 identity, if any.
func (s *Store) LoadIdentity() (*Identity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := readStateFile(s.path)
	if err != nil {
		return nil, err
	}
	if st.SeedB64 == "" {
		return nil, fmt.Errorf("enrollment: no identity persisted")
	}
	seed, err := base64.RawURLEncoding.DecodeString(st.SeedB64)
	if err != nil {
		return nil, fmt.Errorf("enrollment: corrupt identity seed: %w", err)
	}
	return NewIdentityFromSeed(seed)
}

// SaveIdentity persists (generate-if-absent) the identity and returns it.
func (s *Store) SaveIdentity(identity *Identity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := readStateFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if st == nil {
		st = &stateFile{Version: 1}
	}
	st.SeedB64 = base64.RawURLEncoding.EncodeToString(identity.Seed())
	return writeStateFile(s.path, st)
}

// SaveRecord persists the Core-issued enrollment record alongside the identity.
func (s *Store) SaveRecord(record *Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := readStateFile(s.path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if st == nil {
		st = &stateFile{Version: 1}
	}
	st.Record = record
	return writeStateFile(s.path, st)
}

// LoadRecord loads the enrolled record, if any.
func (s *Store) LoadRecord() (*Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := readStateFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrStateNotFound
		}
		return nil, err
	}
	if st.Record == nil {
		return nil, ErrStateNotFound
	}
	return st.Record, nil
}

func readStateFile(path string) (*stateFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st stateFile
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("enrollment: corrupt state file %s: %w", path, err)
	}
	return &st, nil
}

func writeStateFile(path string, st *stateFile) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
