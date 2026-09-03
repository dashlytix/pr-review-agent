// Command dashboard is the entrypoint for the internal admin dashboard
// (internal/dashboard). Like cmd/slackbot and cmd/webhookserver -- and
// unlike cmd/agent, which runs briefly per GitHub Actions event and
// exits -- this is an always-on HTTP process.
//
// It exists to give a dashlytix org admin a safe front door for
// installing the org's GitHub App on whichever repos they choose
// (GitHub's own native install flow does the actual repo picking) and
// to see which installations/repos currently exist -- read live from
// GitHub on every page load, no local database. Access is gated by a
// "Sign in with GitHub" OAuth flow that re-checks live org-admin
// membership on every request, not just at login; see
// internal/dashboard's package doc comment.
//
// Usage:
//
//	GITHUB_APP_ID=123456 GITHUB_APP_PRIVATE_KEY=$(base64 -w0 app.pem) \
//	GITHUB_APP_SLUG=dashlytix-pr-review-agent \
//	GITHUB_OAUTH_CLIENT_ID=Iv1.abc GITHUB_OAUTH_CLIENT_SECRET=... \
//	DASHBOARD_ORG=dashlytix DASHBOARD_SESSION_KEY=$(openssl rand -base64 32) \
//	DASHBOARD_BASE_URL=https://dashboard.internal.example.com \
//	go run ./cmd/dashboard
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	"github.com/dimension/ai-ci-agent/internal/dashboard"
	"github.com/dimension/ai-ci-agent/internal/githubauth"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("dashboard: %v", err)
	}
}

func run() error {
	cfg, err := dashboard.ConfigFromEnv()
	if err != nil {
		return err
	}

	app, err := githubauth.NewAppAuthenticator(cfg.AppID, cfg.AppPrivateKeyPEM)
	if err != nil {
		return fmt.Errorf("build app authenticator: %w", err)
	}

	codec, err := dashboard.NewSessionCodec(cfg.SessionKey)
	if err != nil {
		return fmt.Errorf("build session codec: %w", err)
	}

	handler := &dashboard.Handler{
		App:          app,
		Codec:        codec,
		Org:          cfg.Org,
		AppSlug:      cfg.AppSlug,
		ClientID:     cfg.OAuthClientID,
		ClientSecret: cfg.OAuthClientSecret,
		BaseURL:      cfg.BaseURL,
	}
	server := dashboard.NewServer(cfg.ListenAddr, handler)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("dashboard: listening on %s", cfg.ListenAddr)
		serveErr <- server.Start()
	}()

	select {
	case <-ctx.Done():
		log.Printf("dashboard: shutting down")
		if err := server.Shutdown(context.Background()); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		return nil
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
