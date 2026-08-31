package githubauth

import (
	"context"
	"errors"
	"testing"
	"time"
)

type countingAuthenticator struct {
	calls int
	token string
	err   error
}

func (a *countingAuthenticator) InstallationToken(ctx context.Context, installationID int64) (string, error) {
	a.calls++
	return a.token, a.err
}

func TestInstallationTokenSource_CachesUntilExpiry(t *testing.T) {
	auth := &countingAuthenticator{token: "ghs_cached"}
	src := &InstallationTokenSource{Auth: auth, InstallationID: 1}

	for i := 0; i < 3; i++ {
		tok, err := src.Token(context.Background())
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if tok != "ghs_cached" {
			t.Errorf("call %d: token = %q, want ghs_cached", i, tok)
		}
	}
	if auth.calls != 1 {
		t.Errorf("Authenticator called %d times, want exactly 1 (subsequent calls should hit the cache)", auth.calls)
	}
}

func TestInstallationTokenSource_RefreshesAfterExpiry(t *testing.T) {
	auth := &countingAuthenticator{token: "ghs_first"}
	src := &InstallationTokenSource{Auth: auth, InstallationID: 1}

	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Force the cached entry to look expired without waiting out the
	// real 45-minute TTL.
	src.mu.Lock()
	src.expires = time.Now().Add(-time.Second)
	src.mu.Unlock()

	auth.token = "ghs_second"
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghs_second" {
		t.Errorf("token = %q, want ghs_second after the cached entry expired", tok)
	}
	if auth.calls != 2 {
		t.Errorf("Authenticator called %d times, want exactly 2", auth.calls)
	}
}

func TestInstallationTokenSource_PropagatesAuthenticatorError(t *testing.T) {
	auth := &countingAuthenticator{err: errors.New("app not installed")}
	src := &InstallationTokenSource{Auth: auth, InstallationID: 1}

	if _, err := src.Token(context.Background()); err == nil {
		t.Fatal("expected the Authenticator's error to propagate")
	}
}
