// Package webhook implements the inbound GitHub webhook HTTP endpoint:
// POST /webhooks/github. This is a genuinely new capability -- see
// internal/orchestrate's doc comment -- not a variant of anything that
// existed before. The HTTP layer here (Server, Handler.ServeHTTP,
// signature verification, idempotency) is kept deliberately free of PR
// review logic: every event handler in events.go delegates to
// internal/orchestrate for the actual gather -> assess -> post -> notify
// work, the same functions cmd/agent's own pull_request handling calls.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// signaturePrefix is the fixed prefix GitHub puts on every
// X-Hub-Signature-256 header value.
const signaturePrefix = "sha256="

// VerifySignature reports whether header (the raw X-Hub-Signature-256
// header value) is a valid HMAC-SHA256 signature of body under secret,
// per https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries.
// A missing, malformed, or mismatched signature all report false --
// callers should reject all three identically (401/403) rather than
// distinguishing them, so a probing attacker learns nothing about which
// case they hit.
//
// The comparison is constant-time (hmac.Equal, not ==/bytes.Equal) so a
// timing side-channel can't leak how many leading bytes of a guessed
// signature were correct.
func VerifySignature(secret string, body []byte, header string) bool {
	if secret == "" || header == "" {
		return false
	}
	hexDigest, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexDigest)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)

	return hmac.Equal(got, want)
}

// SignPayload computes the X-Hub-Signature-256 header value GitHub would
// send for body signed with secret. Used by tests (to construct a valid
// signature to verify against) and available for anything that needs to
// emulate a real GitHub delivery, e.g. the manual-cross-repo-review-style
// tooling in this repo.
func SignPayload(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return signaturePrefix + hex.EncodeToString(mac.Sum(nil))
}
