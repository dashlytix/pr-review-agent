package dashboard

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// sessionCookieName and oauthStateCookieName are the two cookies this
// package ever sets. The state cookie is short-lived and carries no
// trusted claims (see beginLogin); the session cookie is the only one
// that grants access.
const (
	sessionCookieName    = "dashboard_session"
	oauthStateCookieName = "dashboard_oauth_state"
)

// sessionTTL bounds how long a session cookie is honored at all,
// independent of the live admin re-check requireAdmin performs on every
// request (see handler.go) -- a hard backstop forcing re-login (and
// thus a fresh OAuth consent + token) periodically, even in a scenario
// where the live check were somehow bypassed.
const sessionTTL = 8 * time.Hour

// session is the entirety of what this dashboard remembers about a
// signed-in user -- sealed inside an encrypted cookie, never held
// server-side (no database, no in-memory session store: this package's
// whole point, per its doc comment). UserToken is the user's own GitHub
// OAuth access token, carried so every subsequent request can re-derive
// (not cache) live org-admin status straight from GitHub -- see
// requireAdmin in handler.go.
type session struct {
	Login     string    `json:"login"`
	UserToken string    `json:"user_token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// sessionCodec seals/opens the session cookie value with AES-256-GCM.
// Encryption -- not a bare HMAC signature -- is required here
// specifically because the sealed payload carries a live bearer
// credential (session.UserToken): confidentiality matters, not merely
// tamper-evidence. GCM's authentication tag already gives integrity for
// free, so there is no separate signing layer to get wrong.
type sessionCodec struct {
	aead cipher.AEAD
}

// NewSessionCodec builds a codec from a 32-byte AES-256 key (see
// Config.SessionKey / DASHBOARD_SESSION_KEY). Exported so
// cmd/dashboard can construct one; the sessionCodec type itself stays
// unexported since nothing outside this package ever needs to name it,
// only hold the pointer this returns.
func NewSessionCodec(key []byte) (*sessionCodec, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("dashboard: session key must be exactly 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("dashboard: build AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("dashboard: build GCM: %w", err)
	}
	return &sessionCodec{aead: aead}, nil
}

// seal encrypts and authenticates s, returning a value safe to use as a
// cookie (base64url, no padding -- cookie-value safe without further
// escaping). s.ExpiresAt must be non-zero; open independently re-checks
// it against the caller-supplied "now" rather than trusting the clock
// this process happened to seal it under.
func (c *sessionCodec) seal(s session) (string, error) {
	if s.ExpiresAt.IsZero() {
		return "", fmt.Errorf("dashboard: refusing to seal a session with no ExpiresAt")
	}
	plaintext, err := json.Marshal(s)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("dashboard: generate nonce: %w", err)
	}

	sealed := c.aead.Seal(nonce, nonce, plaintext, nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

// open reverses seal, rejecting a value that fails authentication
// (tampered or sealed under a different key) or whose ExpiresAt is at
// or before now -- passed in explicitly, not time.Now(), so tests never
// need to wait out a real TTL.
func (c *sessionCodec) open(value string, now time.Time) (session, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return session{}, fmt.Errorf("dashboard: decode session cookie: %w", err)
	}

	nonceSize := c.aead.NonceSize()
	if len(raw) < nonceSize {
		return session{}, fmt.Errorf("dashboard: session cookie shorter than one nonce")
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]

	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return session{}, fmt.Errorf("dashboard: session cookie failed authentication: %w", err)
	}

	var s session
	if err := json.Unmarshal(plaintext, &s); err != nil {
		return session{}, fmt.Errorf("dashboard: decode session payload: %w", err)
	}
	if !now.Before(s.ExpiresAt) {
		return session{}, fmt.Errorf("dashboard: session expired at %s", s.ExpiresAt)
	}
	return s, nil
}
