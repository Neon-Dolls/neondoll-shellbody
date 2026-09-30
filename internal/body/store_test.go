// Tests for durable Shell Body state (identity + WG key + membership).
package body

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/Neon-Dolls/neondoll-shellbody/internal/identity"
)

// testMeta builds a canonical BodyMetadata for tests.
func testMeta() identity.BodyMetadata {
	return identity.BodyMetadata{
		Implementation: "neondoll-shellbody",
		Platform:       "test",
		Architecture:   "test",
		Build:          "test",
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestCreateFresh_ThenReloadSameIdentityAndKey(t *testing.T) {
	t.Helper()
	store := newTestStore(t)
	res, err := store.CreateFresh("Alice", testMeta())
	if err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	bodyID := string(res.State.Identity.BodyID)
	keyB64 := res.Key.PublicKeyBase64()

	// Simulate restart: fresh Store on the same dir loads the same identity +
	// keypair.
	st, kp, err := store.LoadOrError()
	if err != nil {
		t.Fatalf("LoadOrError after CreateFresh: %v", err)
	}
	if string(st.Identity.BodyID) != bodyID {
		t.Fatalf("identity changed across reload: %q vs %q", bodyID, string(st.Identity.BodyID))
	}
	if kp.PublicKeyBase64() != keyB64 {
		t.Fatal("WG public key changed across reload")
	}
}

func TestCreateFresh_CreateOnceUnderConcurrency(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const numContenders = 16
	type result struct {
		won bool
		err error
	}
	ch := make(chan result, numContenders)

	var wg sync.WaitGroup
	wg.Add(numContenders)
	for i := 0; i < numContenders; i++ {
		go func() {
			defer wg.Done()
			r := result{won: false, err: nil}
			if _, err := s.CreateFresh(fmt.Sprintf("race-%d", i), testMeta()); err != nil {
				r.err = err
			} else {
				r.won = true
			}
			ch <- r
		}()
	}
	wg.Wait()
	close(ch)

	winners := 0
	failures := 0
	for r := range ch {
		if r.won {
			winners += 1
		} else if r.err != nil && !errors.Is(r.err, ErrIdentityExists) {
			failures += 1
		}
	}
	if winners != 1 {
		t.Fatalf("expected exactly 1 winner, got %d", winners)
	}
	if failures != 0 {
		t.Fatalf("unexpected failures: %d", failures)
	}
}

func TestCreateFresh_ReportsIdentityExists(t *testing.T) {
	t.Helper()
	store := newTestStore(t)
	if _, err := store.CreateFresh("a", testMeta()); err != nil {
		t.Fatalf("first CreateFresh: %v", err)
	}
	if _, err := store.CreateFresh("b", testMeta()); err == nil {
		t.Fatal("second CreateFresh should fail")
	} else if !errors.Is(err, ErrIdentityExists) {
		t.Fatalf("want ErrIdentityExists, got %v", err)
	}
}

func TestLoadOrError_NoStateReturnsNotFound(t *testing.T) {
	t.Helper()
	store := newTestStore(t)
	_, _, err := store.LoadOrError()
	if err == nil || !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("want ErrStateNotFound, got %v", err)
	}
}

func TestSaveLoadMembership_RoundTrip(t *testing.T) {
	t.Helper()
	store := newTestStore(t)
	if _, err := store.CreateFresh("a", testMeta()); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	m := &Membership{
		NetworkID:    "net_1",
		Status:       MembershipActive,
		BodyPeerID:   "peer_body",
		BodyIPv6:     "fd00::1/128",
		CorePeerID:   "peer_core",
		CoreWGKeyB64: "AAECAwQFBgcICQoLDA0ODw==",
	}
	if err := store.SaveMembership(m); err != nil {
		t.Fatalf("SaveMembership: %v", err)
	}
	loaded, err := store.LoadMembership()
	if err != nil {
		t.Fatalf("LoadMembership: %v", err)
	}
	if loaded.NetworkID != m.NetworkID || loaded.BodyPeerID != m.BodyPeerID ||
		loaded.CorePeerID != m.CorePeerID {
		t.Fatalf("membership round-trip mismatch: %+v vs %+v", loaded, m)
	}
}

func TestLoadMembership_MissingReturnsNotFound(t *testing.T) {
	t.Helper()
	store := newTestStore(t)
	if _, err := store.CreateFresh("a", testMeta()); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	_, err := store.LoadMembership()
	if err == nil || !errors.Is(err, ErrStateNotFound) {
		t.Fatalf("want ErrStateNotFound for missing membership, got %v", err)
	}
}

func TestLoadMembership_CorruptFailsClosed(t *testing.T) {
	t.Helper()
	store := newTestStore(t)
	if _, err := store.CreateFresh("a", testMeta()); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	if err := os.MkdirAll(store.Dir(), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	f, err := os.OpenFile(store.membershipPath(), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open membership: %v", err)
	}
	if _, err := f.Write([]byte("{ not json")); err != nil {
		t.Fatalf("write corrupt membership: %v", err)
	}
	_ = f.Close()

	// A corrupt membership must fail closed (return an error), never be
	// silently replaced or treated as valid.
	if _, err := store.LoadMembership(); err == nil {
		t.Fatal("corrupt membership should fail closed")
	}
}

func TestWgPrivateKeyFile_UsesOwnerPermissions(t *testing.T) {
	t.Helper()
	store := newTestStore(t)
	if _, err := store.CreateFresh("a", testMeta()); err != nil {
		t.Fatalf("CreateFresh: %v", err)
	}
	st, err := os.Stat(store.wgPrivatePath())
	if err != nil {
		t.Fatalf("wg private key file missing: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Fatalf("wg private key must be owner-only, got mode %o", perm)
	}
}
