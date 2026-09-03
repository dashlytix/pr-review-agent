// Package dashboard is a small internal admin tool: it lets a dashlytix
// org admin (verified live via GitHub's own org-membership API on every
// request -- see session.go/github.go) install the org's GitHub App on
// whichever repos they choose, and see which installations/repos
// currently exist. It holds no local storage of its own -- every
// installation/repo list is fetched live from GitHub on each page load,
// the same "re-derive context every invocation" principle ADR-001 §3
// applies to the rest of this repo, extended here to authorization
// itself: an admin demoted mid-session loses dashboard access on their
// very next click, not just after their session cookie expires.
package dashboard

import (
	"encoding/base64"
	"fmt"
	"os"
)

// defaultListenAddr differs from cmd/webhookserver's :8080 default so
// the two can run on the same host without an operator needing to
// override either.
const defaultListenAddr = ":8081"

// Config is this binary's own configuration, resolved from the
// environment the same way cmd/webhookserver/cmd/slackbot resolve
// theirs -- plain os.Getenv reads in the entrypoint, not the
// Actions-input-prefixed pattern cmd/agent uses, since this is an
// always-on process, not an Action step.
type Config struct {
	// AppID and AppPrivateKeyPEM build the githubauth.AppAuthenticator
	// used for every GitHub-App-authenticated call (ListInstallations,
	// ListInstallationRepositories). Same App as GITHUB_WEBHOOK_SECRET's
	// deliveries are eventually meant to authenticate through -- see
	// internal/githubauth's package doc comment.
	AppID            string
	AppPrivateKeyPEM []byte
	// AppSlug builds the public install-flow URL
	// (https://github.com/apps/<slug>/installations/new). Not fetched
	// live via GET /app -- it's static and already known from the App's
	// own settings page, so a live call for it on every page load would
	// be a pointless round-trip.
	AppSlug string

	// OAuthClientID/OAuthClientSecret are the App's built-in "Sign in
	// with GitHub App" OAuth credentials -- a different credential pair
	// from AppID/AppPrivateKeyPEM, found on the same App settings page.
	OAuthClientID     string
	OAuthClientSecret string

	// Org is the GitHub organization a signed-in user must be an active
	// admin of to see anything past the login page.
	Org string

	// SessionKey is a 32-byte AES-256 key (base64-encoded in the
	// environment) used to seal/open the session cookie -- see
	// session.go. It carries a live user OAuth token, so this must
	// encrypt, not just sign.
	SessionKey []byte

	// BaseURL is this server's own externally-reachable origin (e.g.
	// https://dashboard.internal.example.com, no trailing slash) --
	// used to build the OAuth redirect_uri and to decide whether cookies
	// get the Secure attribute.
	BaseURL string

	// ListenAddr is the address (host:port) the HTTP server binds to.
	// Defaults to ":8081" if unset.
	ListenAddr string
}

// ConfigFromEnv reads every required GITHUB_APP_*/GITHUB_OAUTH_*/
// DASHBOARD_* variable, base64-decoding GITHUB_APP_PRIVATE_KEY and
// DASHBOARD_SESSION_KEY, and fails fast (matching this repo's existing
// "missing config fails fast rather than degrading silently" precedent)
// if anything required is missing or malformed.
func ConfigFromEnv() (Config, error) {
	required := map[string]string{
		"GITHUB_APP_ID":              os.Getenv("GITHUB_APP_ID"),
		"GITHUB_APP_PRIVATE_KEY":     os.Getenv("GITHUB_APP_PRIVATE_KEY"),
		"GITHUB_APP_SLUG":            os.Getenv("GITHUB_APP_SLUG"),
		"GITHUB_OAUTH_CLIENT_ID":     os.Getenv("GITHUB_OAUTH_CLIENT_ID"),
		"GITHUB_OAUTH_CLIENT_SECRET": os.Getenv("GITHUB_OAUTH_CLIENT_SECRET"),
		"DASHBOARD_ORG":              os.Getenv("DASHBOARD_ORG"),
		"DASHBOARD_SESSION_KEY":      os.Getenv("DASHBOARD_SESSION_KEY"),
		"DASHBOARD_BASE_URL":         os.Getenv("DASHBOARD_BASE_URL"),
	}
	for name, v := range required {
		if v == "" {
			return Config{}, fmt.Errorf("dashboard: missing required environment variable %s", name)
		}
	}

	privateKey, err := decodeBase64Env("GITHUB_APP_PRIVATE_KEY", required["GITHUB_APP_PRIVATE_KEY"])
	if err != nil {
		return Config{}, err
	}
	sessionKey, err := decodeBase64Env("DASHBOARD_SESSION_KEY", required["DASHBOARD_SESSION_KEY"])
	if err != nil {
		return Config{}, err
	}
	if len(sessionKey) != 32 {
		return Config{}, fmt.Errorf("dashboard: DASHBOARD_SESSION_KEY must decode to exactly 32 bytes (AES-256), got %d", len(sessionKey))
	}

	addr := os.Getenv("DASHBOARD_LISTEN_ADDR")
	if addr == "" {
		addr = defaultListenAddr
	}

	return Config{
		AppID:             required["GITHUB_APP_ID"],
		AppPrivateKeyPEM:  privateKey,
		AppSlug:           required["GITHUB_APP_SLUG"],
		OAuthClientID:     required["GITHUB_OAUTH_CLIENT_ID"],
		OAuthClientSecret: required["GITHUB_OAUTH_CLIENT_SECRET"],
		Org:               required["DASHBOARD_ORG"],
		SessionKey:        sessionKey,
		BaseURL:           required["DASHBOARD_BASE_URL"],
		ListenAddr:        addr,
	}, nil
}

// decodeBase64Env base64-decodes an environment variable's value,
// naming it in the error so a malformed GITHUB_APP_PRIVATE_KEY or
// DASHBOARD_SESSION_KEY is easy to trace back to its source. Standard
// (not URL-safe) base64 -- what `base64` on the command line and
// `openssl rand -base64 32` both produce.
func decodeBase64Env(name, value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("dashboard: %s is not valid base64: %w", name, err)
	}
	return decoded, nil
}
