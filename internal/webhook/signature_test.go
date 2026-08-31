package webhook

import "testing"

const testSecret = "test-webhook-secret"

func TestVerifySignature_Valid(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	sig := SignPayload(testSecret, body)

	if !VerifySignature(testSecret, body, sig) {
		t.Error("expected a correctly signed payload to verify")
	}
}

func TestVerifySignature_Invalid(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	if VerifySignature(testSecret, body, "sha256=0000000000000000000000000000000000000000000000000000000000000000") {
		t.Error("expected a wrong-but-well-formed signature to fail verification")
	}
}

func TestVerifySignature_Missing(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	if VerifySignature(testSecret, body, "") {
		t.Error("expected a missing signature header to fail verification")
	}
}

func TestVerifySignature_ModifiedPayload(t *testing.T) {
	original := []byte(`{"action":"opened"}`)
	sig := SignPayload(testSecret, original)

	tampered := []byte(`{"action":"closed"}`)
	if VerifySignature(testSecret, tampered, sig) {
		t.Error("expected a signature computed over a different payload to fail verification")
	}
}

func TestVerifySignature_EmptyPayload(t *testing.T) {
	body := []byte{}
	sig := SignPayload(testSecret, body)

	if !VerifySignature(testSecret, body, sig) {
		t.Error("expected a correctly signed empty payload to verify")
	}
	if VerifySignature(testSecret, []byte("not empty"), sig) {
		t.Error("expected the empty-body signature to fail against a non-empty body")
	}
}

func TestSignPayload_MatchesGitHubFormat(t *testing.T) {
	sig := SignPayload(testSecret, []byte("hello"))
	if len(sig) != len("sha256=")+64 {
		t.Errorf("SignPayload output length = %d, want %q-prefix + 64 hex chars", len(sig), "sha256=")
	}
	if sig[:7] != "sha256=" {
		t.Errorf("SignPayload = %q, want sha256= prefix", sig)
	}
}

func TestVerifySignature_MalformedPrefixRejected(t *testing.T) {
	body := []byte("hello")
	// A valid hex digest, but with GitHub's older sha1= scheme's prefix
	// instead of sha256= -- must not be accepted just because the hex
	// portion happens to be well-formed.
	if VerifySignature(testSecret, body, "sha1=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Error("expected a non-sha256= prefixed header to fail verification")
	}
}

func TestVerifySignature_NonHexDigestRejected(t *testing.T) {
	if VerifySignature(testSecret, []byte("hello"), "sha256=not-hex!!") {
		t.Error("expected a non-hex digest to fail verification rather than panic or false-positive")
	}
}

func TestVerifySignature_EmptySecretAlwaysRejects(t *testing.T) {
	body := []byte("hello")
	sig := SignPayload("", body)
	if VerifySignature("", body, sig) {
		t.Error("an empty configured secret must never validate any signature -- that would accept an unconfigured deployment's every request")
	}
}
