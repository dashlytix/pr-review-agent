package dashboard

import (
	"encoding/base64"
	"strings"
	"testing"
)

func validEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"GITHUB_APP_ID":              "12345",
		"GITHUB_APP_PRIVATE_KEY":     base64.StdEncoding.EncodeToString([]byte("fake-pem-contents")),
		"GITHUB_APP_SLUG":            "dashlytix-pr-review-agent",
		"GITHUB_OAUTH_CLIENT_ID":     "Iv1.abc123",
		"GITHUB_OAUTH_CLIENT_SECRET": "secret",
		"DASHBOARD_ORG":              "dashlytix",
		"DASHBOARD_SESSION_KEY":      base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"DASHBOARD_BASE_URL":         "https://dashboard.internal.example.com",
	}
}

func withEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
}

func TestConfigFromEnv_HappyPath(t *testing.T) {
	withEnv(t, validEnv(t))

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AppID != "12345" {
		t.Errorf("AppID = %q, want 12345", cfg.AppID)
	}
	if string(cfg.AppPrivateKeyPEM) != "fake-pem-contents" {
		t.Errorf("AppPrivateKeyPEM = %q, want the decoded fake PEM", cfg.AppPrivateKeyPEM)
	}
	if len(cfg.SessionKey) != 32 {
		t.Errorf("len(SessionKey) = %d, want 32", len(cfg.SessionKey))
	}
	if cfg.ListenAddr != defaultListenAddr {
		t.Errorf("ListenAddr = %q, want the default %q", cfg.ListenAddr, defaultListenAddr)
	}
}

func TestConfigFromEnv_ListenAddrOverride(t *testing.T) {
	env := validEnv(t)
	env["DASHBOARD_LISTEN_ADDR"] = ":9999"
	withEnv(t, env)

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ListenAddr != ":9999" {
		t.Errorf("ListenAddr = %q, want :9999", cfg.ListenAddr)
	}
}

func TestConfigFromEnv_MissingRequiredVarFailsFast(t *testing.T) {
	for missing := range validEnv(t) {
		t.Run(missing, func(t *testing.T) {
			env := validEnv(t)
			delete(env, missing)
			withEnv(t, env)
			t.Setenv(missing, "")

			if _, err := ConfigFromEnv(); err == nil {
				t.Fatalf("expected an error when %s is missing", missing)
			} else if !strings.Contains(err.Error(), missing) {
				t.Errorf("error = %v, want it to name %s", err, missing)
			}
		})
	}
}

func TestConfigFromEnv_MalformedBase64PrivateKeyIsAnError(t *testing.T) {
	env := validEnv(t)
	env["GITHUB_APP_PRIVATE_KEY"] = "not valid base64!!"
	withEnv(t, env)

	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected an error for malformed base64 in GITHUB_APP_PRIVATE_KEY")
	} else if !strings.Contains(err.Error(), "GITHUB_APP_PRIVATE_KEY") {
		t.Errorf("error = %v, want it to name GITHUB_APP_PRIVATE_KEY", err)
	}
}

func TestConfigFromEnv_WrongLengthSessionKeyIsAnError(t *testing.T) {
	env := validEnv(t)
	env["DASHBOARD_SESSION_KEY"] = base64.StdEncoding.EncodeToString([]byte("too-short"))
	withEnv(t, env)

	if _, err := ConfigFromEnv(); err == nil {
		t.Fatal("expected an error for a session key that isn't 32 bytes")
	} else if !strings.Contains(err.Error(), "32 bytes") {
		t.Errorf("error = %v, want it to explain the required length", err)
	}
}
