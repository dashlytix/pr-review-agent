// Command webhookserver is the entrypoint for the inbound GitHub webhook
// HTTP server (internal/webhook). Like cmd/slackbot -- and unlike
// cmd/agent, which runs briefly per GitHub Actions event and exits --
// this is an always-on process: GitHub delivers a webhook once, over a
// short-lived HTTP request, to whatever is listening at the configured
// URL, so something has to already be listening.
//
// Authentication today is a plain PAT (GITHUB_TOKEN), the same as every
// other entrypoint in this repo -- see internal/githubauth for the
// GitHub-App-backed alternative this is deliberately structured to swap
// in later without any change to internal/gather, internal/post, or
// internal/orchestrate. One token is shared across every repository this
// server receives webhooks for (see internal/webhook.Handler.client) --
// it must have access to all of them, since there is no per-repo
// installation token yet.
//
// Usage:
//
//	GITHUB_WEBHOOK_SECRET=whsec_... GITHUB_TOKEN=ghp_... \
//	LLM_PROVIDER=claude LLM_API_KEY=sk-... \
//	go run ./cmd/webhookserver
//
// Register the same webhook URL (with the same GITHUB_WEBHOOK_SECRET) on
// as many repositories as should be served by this one process -- the
// target repository is read from each delivery's own payload, not a
// fixed GITHUB_REPOSITORY value.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/dimension/ai-ci-agent/internal/notify"
	"github.com/dimension/ai-ci-agent/internal/provider"
	"github.com/dimension/ai-ci-agent/internal/webhook"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("webhookserver: %v", err)
	}
}

func run() error {
	webhookCfg, err := webhook.ConfigFromEnv()
	if err != nil {
		return err
	}

	githubToken := os.Getenv("GITHUB_TOKEN")
	providerName := envOr("LLM_PROVIDER", "claude")
	apiKey := os.Getenv("LLM_API_KEY")

	for name, v := range map[string]string{
		"GITHUB_TOKEN": githubToken,
		"LLM_API_KEY":  apiKey,
	} {
		if v == "" {
			return fmt.Errorf("missing required environment variable %s", name)
		}
	}

	llmProvider, err := provider.Get(providerName, apiKey)
	if err != nil {
		return err
	}

	slackCfg := notify.SlackConfig{
		BotToken: os.Getenv("SLACK_BOT_TOKEN"),
		Channel:  os.Getenv("SLACK_CHANNEL"),
	}

	handler := &webhook.Handler{
		Secret:      webhookCfg.WebhookSecret,
		Token:       githubToken,
		Provider:    llmProvider,
		SlackConfig: slackCfg,
	}
	server := webhook.NewServer(webhookCfg.ListenAddr, handler)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("webhookserver: listening on %s (POST /webhooks/github)", webhookCfg.ListenAddr)
		serveErr <- server.Start()
	}()

	select {
	case <-ctx.Done():
		log.Printf("webhookserver: shutting down")
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
