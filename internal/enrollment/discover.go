// Core identity discovery and trust-anchor resolution.
//
// Before enrollment a Shell Body may obtain Core's public bootstrap identity
// from GET {baseURL}/v1/core/identity. That response is DISCOVERY material,
// not trust: it is unauthenticated transport data and never by itself an
// authority. Trust is established only by the owner-approved enrollment
// ceremony — the Body redeems a Core-issued invitation, Core signs the receipt
// with the private half of the very key the Body has pinned, the Body verifies
// that signature, and the receipt's signed CoreID must equal the resolved Core
// ID. Discovery merely supplies the untrusted key/ID claim that this signature
// check then binds cryptographically.
package enrollment

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	// maxIdentityResponseBytes bounds an inbound identity document.
	maxIdentityResponseBytes = 1 << 20
	// discoverTimeout bounds a single identity GET.
	discoverTimeout = 30 * time.Second
)

// ErrCoreIdentityRejected reports that a Core identity claim was rejected:
// an unsupported version/algorithm, a missing CoreID, a public key that does
// not decode to 32 bytes, a mismatch against the operator's explicit
// --core-key/--core-id, or an attempt to silently replace an existing pinned
// Core trust anchor. It is a security boundary rejection, distinct from a
// network/HTTP failure.
var ErrCoreIdentityRejected = errors.New("core identity: rejected")

// CoreTrustResolution is the outcome of resolving Core's trust anchor before
// an enrollment: the CoreID to present and the 32-byte Ed25519 public key to
// verify the enrollment receipt against.
type CoreTrustResolution struct {
	CoreID    string
	PublicKey []byte
}

// DiscoverCoreIdentity fetches and validates Core's public identity document.
// It returns exactly the validated claim; the caller decides how to bind it.
// hc may be nil to use a default client. On failure: ErrCoreIdentityRejected
// (chained as the cause) when the claim was invalid, or a transport/HTTP error
// when Core was unreachable.
func DiscoverCoreIdentity(ctx context.Context, hc *http.Client, baseURL string) (*CoreIdentityResponse, error) {
	if hc == nil {
		hc = &http.Client{Timeout: discoverTimeout}
	}
	raw, err := getBody(ctx, hc, baseURL)
	if err != nil {
		return nil, err
	}
	var id CoreIdentityResponse
	if err := json.Unmarshal(raw, &id); err != nil {
		return nil, fmt.Errorf("%w: malformed document: %w", ErrCoreIdentityRejected, err)
	}
	if err := ValidateCoreIdentity(id); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCoreIdentityRejected, err)
	}
	return &id, nil
}

// ValidateCoreIdentity enforces the identity document contract: version must
// equal CoreIdentityVersion, algorithm must equal CoreIdentityAlgorithm, the
// CoreID must be non-empty, and public_key must be the 32-byte Ed25519 key
// encoding (unpadded base64url). Any violation is a hard rejection.
func ValidateCoreIdentity(id CoreIdentityResponse) error {
	if id.Version != CoreIdentityVersion {
		return fmt.Errorf("unsupported identity version %d (want %d)", id.Version, CoreIdentityVersion)
	}
	if id.Algorithm != CoreIdentityAlgorithm {
		return fmt.Errorf("unsupported algorithm %q (want %q)", id.Algorithm, CoreIdentityAlgorithm)
	}
	if id.CoreID == "" {
		return errors.New("missing core_id")
	}
	if _, err := decodeEd25519Pubkey(id.PublicKey); err != nil {
		return err
	}
	return nil
}

// decodeEd25519Pubkey decodes an unpadded-base64url string to an Ed25519 public
// key (exactly ed25519.PublicKeySize bytes), or rejects it.
func decodeEd25519Pubkey(b64 string) ([]byte, error) {
	raw, err := DecodeRawURL(b64)
	if err != nil {
		return nil, fmt.Errorf("public_key is not valid base64url: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public_key decodes to %d bytes, want %d (Ed25519)", len(raw), ed25519.PublicKeySize)
	}
	return raw, nil
}

// ResolveCoreTrust applies the trust policy for a new enrollment and returns
// the CoreID + public key to pin, or a hard rejection.
//
// Rules:
//  1. Discovery is mandatory unless an explicit --core-key AND --core-id are
//     supplied; without an explicit anchor, an unreachable Core fails closed
//     (no automatic trust-on-first-use during unattended operation).
//  2. When discovery succeeds, any explicit --core-key/--core-id are CROSS-
//     CHECKED against the live document; a mismatch is a hard rejection.
//  3. A reachable Core serving a structurally invalid document is always a
//     hard rejection, even with an explicit anchor supplied.
//  4. An existing pinned enrollment (existing) is never silently replaced: if
//     its CoreID or public key differs from the resolved identity, enrollment
//     fails.
//
// ctx is required for the network call. hc may be nil for a default client.
func ResolveCoreTrust(ctx context.Context, hc *http.Client, baseURL, explicitCoreID, explicitKeyRaw string, existing *Record) (*CoreTrustResolution, error) {
	if baseURL == "" {
		return nil, errors.New("discovery requires a Core URL")
	}
	if ctx == nil {
		return nil, errors.New("discovery requires a context")
	}
	var explicitPub *[]byte
	if explicitKeyRaw != "" {
		pub, err := decodeEd25519Pubkey(explicitKeyRaw)
		if err != nil {
			return nil, err
		}
		explicitPub = &pub
	}

	discovered, err := DiscoverCoreIdentity(ctx, hc, baseURL)
	if err != nil {
		if errors.Is(err, ErrCoreIdentityRejected) {
			// Core is reachable but the document is invalid or not a valid Ed25519
			// identity. Always fail closed, even with an explicit anchor: a
			// malformed document from the intended Core is never acceptable — and
			// tolerating it would silently weaken the trust binding.
			return nil, err
		}
		// Core unreachable. Proceed only with an explicit anchor + core ID;
		// otherwise fail closed (no silent TOFU in unattended operation).
		if explicitPub == nil || explicitCoreID == "" {
			return nil, err
		}
		// The explicit anchor is a valid trust input, but it is still subject
		// to the same pinning checks as any other successful resolution:
		// never silently replace an existing enrollment's trust anchor.
		resolution := CoreTrustResolution{CoreID: explicitCoreID, PublicKey: *explicitPub}
		if perr := checkPinnedAnchor(existing, &resolution); perr != nil {
			return nil, perr
		}
		return &resolution, nil
	}

	// Discovery succeeded: cross-check any explicit expectations.
	if explicitPub != nil {
		discPub, _ := decodeEd25519Pubkey(discovered.PublicKey)
		if !pubBytesEqual(*explicitPub, discPub) {
			return nil, fmt.Errorf("%w: --core-key does not match the discovered Core identity (refusing to enroll against a mismatched trust anchor)", ErrCoreIdentityRejected)
		}
	}
	if explicitCoreID != "" && explicitCoreID != discovered.CoreID {
		return nil, fmt.Errorf("%w: --core-id %q does not match the discovered Core identity %q", ErrCoreIdentityRejected, explicitCoreID, discovered.CoreID)
	}

	resolvedPub, _ := decodeEd25519Pubkey(discovered.PublicKey)
	resolution := CoreTrustResolution{CoreID: discovered.CoreID, PublicKey: resolvedPub}

	// Never silently replace an existing trust anchor.
	if perr := checkPinnedAnchor(existing, &resolution); perr != nil {
		return nil, perr
	}
	return &resolution, nil
}

// checkPinnedAnchor applies the trust-pinning invariants to a resolved trust
// anchor (discovered or explicitly supplied). It must run on EVERY successful
// resolution path so that an existing enrollment is never silently redirected
// to a different Core. It also fails closed if the stored anchor is corrupt:
// a persisted but malformed CorePublicKey is never ignored, it is rejected.
func checkPinnedAnchor(existing *Record, resolution *CoreTrustResolution) error {
	if existing == nil {
		return nil
	}
	if existing.CoreID != "" && existing.CoreID != resolution.CoreID {
		return fmt.Errorf("%w: refusing to replace existing enrollment pinned to core %q with core %q (move Cores by enrolling into a fresh state directory)", ErrCoreIdentityRejected, existing.CoreID, resolution.CoreID)
	}
	if existing.CorePublicKey != "" {
		exPub, dErr := decodeEd25519Pubkey(existing.CorePublicKey)
		if dErr != nil {
			// A persisted but malformed trust anchor must never be silently
			// ignored or replaced: fail closed on the corrupt stored key.
			return fmt.Errorf("%w: existing enrollment stores an invalid core trust anchor (corrupt public key); refusing to resolve a replacement", ErrCoreIdentityRejected)
		}
		if !pubBytesEqual(exPub, resolution.PublicKey) {
			return fmt.Errorf("%w: refusing to replace the existing core trust anchor with a different public key", ErrCoreIdentityRejected)
		}
	}
	return nil
}

// pubBytesEqual compares two fixed-size byte buffers (public keys).
func pubBytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// getBody performs an unauthenticated GET of /v1/core/identity and returns the
// raw body. Non-2xx responses are decoded as an EnrollmentErrorResponse.
func getBody(ctx context.Context, hc *http.Client, baseURL string) ([]byte, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/core/identity", bytes.NewReader(make([]byte, 0)))
	if err != nil {
		return nil, fmt.Errorf("core identity: build request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/json")
	resp, err := hc.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("core identity: GET /v1/core/identity: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIdentityResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("core identity: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e EnrollmentErrorResponse
		_ = json.Unmarshal(body, &e)
		code := e.Error.Code
		if code == "" {
			code = "unavailable"
		}
		return nil, fmt.Errorf("core identity: GET /v1/core/identity: %s (%d): %s", code, resp.StatusCode, e.Error.Message)
	}
	return body, nil
}
