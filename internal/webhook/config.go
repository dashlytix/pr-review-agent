package webhook

import (
	"fmt"
	"os"
)

// defaultListenAddr matches this repo's other always-on daemon
// (cmd/slackbot) in spirit: a sane default that still needs no
// configuration to run locally.
const defaultListenAddr = ":8080"

// Config is the webhook server's own configuration, resolved from the
// environment the same way cmd/slackbot resolves its config -- plain
// os.Getenv reads in the entrypoint (see cmd/webhookserver/main.go),
// not the Actions-input-prefixed pattern cmd/agent uses, since this is
// an always-on process, not an Action step.
type Config struct {
	// WebhookSecret is the shared secret configured on the GitHub
	// App/webhook, used to verify X-Hub-Signature-256. Required --
	// ConfigFromEnv fails fast if it's empty, the same "fail fast
	// instead of silently degrading" precedent §7 sets for a missing
	// provider key.
	WebhookSecret string
	// ListenAddr is the address (host:port) the HTTP server binds to.
	// Defaults to ":8080" if unset.
	ListenAddr string
}

// ConfigFromEnv reads GITHUB_WEBHOOK_SECRET (required) and
// WEBHOOK_LISTEN_ADDR (optional, defaults to ":8080").
func ConfigFromEnv() (Config, error) {
	secret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	if secret == "" {
		return Config{}, fmt.Errorf("webhook: missing required environment variable GITHUB_WEBHOOK_SECRET")
	}

	addr := os.Getenv("WEBHOOK_LISTEN_ADDR")
	if addr == "" {
		addr = defaultListenAddr
	}

	return Config{WebhookSecret: secret, ListenAddr: addr}, nil
}
