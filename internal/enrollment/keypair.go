package enrollment

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
)

// Identity holds the Body's Ed25519 identity keypair used to sign enrollment
// proof-of-possession transcripts. Only the public key ever leaves the Body;
// the private key is retained locally.
type Identity struct {
	privateKey ed25519.PrivateKey
}

// NewIdentity generates a fresh Ed25519 keypair.
func NewIdentity() (*Identity, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("enrollment: generate identity: %w", err)
	}
	return &Identity{privateKey: priv}, nil
}

// NewIdentityFromSeed rebuilds an Identity from a persisted 32-byte seed.
func NewIdentityFromSeed(seed []byte) (*Identity, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("enrollment: ed25519 seed must be %d bytes, got %d", ed25519.SeedSize, len(seed))
	}
	return &Identity{privateKey: ed25519.NewKeyFromSeed(seed)}, nil
}

// PrivateKey returns the signing key (used internally for transcripts).
func (i *Identity) PrivateKey() ed25519.PrivateKey { return i.privateKey }

// PublicKey returns the Ed25519 public key.
func (i *Identity) PublicKey() ed25519.PublicKey {
	return i.privateKey.Public().(ed25519.PublicKey)
}

// PublicKeyRawURL is the canonical base64url wire form of the public key.
func (i *Identity) PublicKeyRawURL() string { return EncodeRawURL(i.PublicKey()) }

// Fingerprint is hex(sha256(public key)), the stable wire identity label.
func (i *Identity) Fingerprint() string { return BodyKeyFingerprint(i.PublicKey()) }

// Seed returns the 32-byte seed for durable persistence.
func (i *Identity) Seed() []byte { return i.privateKey.Seed() }
