package dashboard

import (
	"strings"
	"testing"
	"time"
)

func testSessionKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}

func TestSessionCodec_SealOpenRoundTrip(t *testing.T) {
	codec, err := NewSessionCodec(testSessionKey())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	now := time.Now()
	want := session{Login: "octocat", UserToken: "gho_abc123", ExpiresAt: now.Add(time.Hour)}

	sealed, err := codec.seal(want)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := codec.open(sealed, now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got.Login != want.Login || got.UserToken != want.UserToken {
		t.Errorf("got = %+v, want %+v", got, want)
	}
}

func TestSessionCodec_RejectsTamperedCiphertext(t *testing.T) {
	codec, err := NewSessionCodec(testSessionKey())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sealed, err := codec.seal(session{Login: "octocat", UserToken: "gho_abc", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	tampered := []byte(sealed)
	tampered[len(tampered)-1] ^= 0x01 // flip a bit near the end (inside the auth tag)
	if _, err := codec.open(string(tampered), time.Now()); err == nil {
		t.Fatal("expected tampered ciphertext to fail authentication")
	}
}

func TestSessionCodec_RejectsExpiredSession(t *testing.T) {
	codec, err := NewSessionCodec(testSessionKey())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	now := time.Now()
	sealed, err := codec.seal(session{Login: "octocat", UserToken: "gho_abc", ExpiresAt: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	if _, err := codec.open(sealed, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expected an expired session to be rejected")
	} else if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error = %v, want it to mention expiry", err)
	}

	// Not yet expired at exactly the boundary instant should still open.
	if _, err := codec.open(sealed, now.Add(30*time.Second)); err != nil {
		t.Errorf("expected a not-yet-expired session to open, got: %v", err)
	}
}

func TestSessionCodec_RejectsWrongKey(t *testing.T) {
	codec, err := NewSessionCodec(testSessionKey())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sealed, err := codec.seal(session{Login: "octocat", UserToken: "gho_abc", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	otherKey := testSessionKey()
	otherKey[0] ^= 0xFF
	otherCodec, err := NewSessionCodec(otherKey)
	if err != nil {
		t.Fatalf("unexpected error building the other codec: %v", err)
	}

	if _, err := otherCodec.open(sealed, time.Now()); err == nil {
		t.Fatal("expected a session sealed under a different key to fail authentication")
	}
}

func TestSessionCodec_RejectsNonThirtyTwoByteKey(t *testing.T) {
	if _, err := NewSessionCodec([]byte("too-short")); err == nil {
		t.Fatal("expected an error for a key that isn't 32 bytes")
	}
}

func TestSessionCodec_RefusesToSealWithoutExpiresAt(t *testing.T) {
	codec, err := NewSessionCodec(testSessionKey())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := codec.seal(session{Login: "octocat", UserToken: "gho_abc"}); err == nil {
		t.Fatal("expected an error when ExpiresAt is the zero value")
	}
}
