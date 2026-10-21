package enrollment

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	// maxEnrollmentResponseBytes bounds inbound response bodies.
	maxEnrollmentResponseBytes = 1 << 20
	// defaultHTTPTimeout bounds a single enrollment HTTP round trip.
	defaultHTTPTimeout = 30 * time.Second
)

// Client performs Core 4 enrollment HTTP requests. One (requestID, nonce)
// pair is generated per attempt and MUST be reused across a flow (Core keys
// result polls and anti-replay checks on them).
type Client struct {
	HTTP           *http.Client
	BaseURL        string
	CoreID         string
	Identity       *Identity
	WGPublicKeyStd string // 32-byte X25519 public key, STANDARD base64 (Doll Network wg_public_key)

	mu        sync.Mutex
	requestID string
	nonceRaw  string
	closed    bool
}

// NewClient constructs an enrollment client against Core's base URL.
// wgPublicKeyStd is the Body's X25519 public key in STANDARD base64 (RFC 4648)
// — the canonical Doll Network wg_public_key encoding.
func NewClient(baseURL, coreID string, identity *Identity, wgPublicKeyStd string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Client{
		HTTP:           hc,
		BaseURL:        baseURL,
		CoreID:         coreID,
		Identity:       identity,
		WGPublicKeyStd: wgPublicKeyStd,
	}
}

// CoreID exposes the configured core identity.
func (c *Client) CoreIdentity() string { return c.CoreID }

// Begin starts a body_request-mode enrollment (challenge + optional approval
// code). Returns the Core-signed pending challenge.
func (c *Client) Begin(ctx context.Context) (*EnrollmentPendingResponse, error) {
	c.newAttempt()
	req := EnrollmentBeginRequest{
		Version:       EnrollmentVersion,
		RequestID:     c.currentRequestID(),
		CoreID:        c.CoreID,
		BodyPublicKey: c.Identity.PublicKeyRawURL(),
		WGPublicKey:   c.WGPublicKeyStd,
		BodyNonce:     c.currentNonce(),
	}
	if err := SignEnrollmentBegin(c.Identity.PrivateKey(), &req); err != nil {
		return nil, err
	}
	var resp EnrollmentPendingResponse
	if err := c.post(ctx, "/v1/body/enrollment/begin", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Redeem registers with Core using an invitation. Core responds with a signed
// EnrollmentReceipt directly. This is the canonical register path.
func (c *Client) Redeem(ctx context.Context, invitationID, invitationSecret string) (*EnrollmentReceipt, error) {
	c.newAttempt()
	req := EnrollmentRedeemRequest{
		Version:          EnrollmentVersion,
		RequestID:        c.currentRequestID(),
		CoreID:           c.CoreID,
		InvitationID:     invitationID,
		InvitationSecret: invitationSecret,
		BodyPublicKey:    c.Identity.PublicKeyRawURL(),
		WGPublicKey:      c.WGPublicKeyStd,
		BodyNonce:        c.currentNonce(),
	}
	if err := SignEnrollmentRedeem(c.Identity.PrivateKey(), &req); err != nil {
		return nil, err
	}
	var receipt EnrollmentReceipt
	if err := c.post(ctx, "/v1/body/enrollment/redeem", req, &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

// Result polls the terminal enrollment status using the same requestID + nonce
// established by Begin or Redeem. When Status == enrolled it carries the
// receipt.
func (c *Client) Result(ctx context.Context) (*EnrollmentResultResponse, error) {
	req := EnrollmentResultRequest{
		Version:       EnrollmentVersion,
		RequestID:     c.currentRequestID(),
		CoreID:        c.CoreID,
		BodyPublicKey: c.Identity.PublicKeyRawURL(),
		BodyNonce:     c.currentNonce(),
	}
	if err := SignEnrollmentResult(c.Identity.PrivateKey(), &req); err != nil {
		return nil, err
	}
	var resp EnrollmentResultResponse
	if err := c.post(ctx, "/v1/body/enrollment/result", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// newAttempt mints a fresh (requestID, nonce) pair for the current flow.
func (c *Client) newAttempt() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requestID = randomID("erq")
	c.nonceRaw = EncodeRawURL(randomBytes(nonceSize))
}

func (c *Client) currentRequestID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requestID
}

func (c *Client) currentNonce() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nonceRaw
}

// post marshals request, POSTs to BaseURL+path, and decodes into response.
// Non-2xx responses are decoded as an EnrollmentErrorResponse.
func (c *Client) post(ctx context.Context, path string, request, response any) error {
	raw, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("enrollment: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("enrollment: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return fmt.Errorf("enrollment: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxEnrollmentResponseBytes))
	if err != nil {
		return fmt.Errorf("enrollment: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e EnrollmentErrorResponse
		_ = json.Unmarshal(body, &e)
		code := e.Error.Code
		if code == "" {
			code = "unavailable"
		}
		return fmt.Errorf("enrollment: POST %s: %s (%d): %s", path, code, resp.StatusCode, e.Error.Message)
	}
	if err := json.Unmarshal(body, response); err != nil {
		return fmt.Errorf("enrollment: decode response: %w", err)
	}
	return nil
}

func randomID(prefix string) string {
	return prefix + "_" + EncodeRawURL(randomBytes(16))
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Crypto/rand failure is fatal for enrollment security.
		panic(fmt.Sprintf("enrollment: crypto/rand: %v", err))
	}
	return b
}
