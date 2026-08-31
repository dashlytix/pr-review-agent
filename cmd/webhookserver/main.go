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
// internal/orchestrate.
//
// Usage:
//
//	GITHUB_WEBHOOK_SECRET=whsec_... GITHUB_TOKEN=ghp_... GITHUB_REPOSITORY=owner/repo \
//	LLM_PROVIDER=claude LLM_API_KEY=sk-... \
//	go run ./cmd/webhookserver
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
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
	repoFull := os.Getenv("GITHUB_REPOSITORY")
	providerName := envOr("LLM_PROVIDER", "claude")
	apiKey := os.Getenv("LLM_API_KEY")

	for name, v := range map[string]string{
		"GITHUB_TOKEN":      githubToken,
		"GITHUB_REPOSITORY": repoFull,
		"LLM_API_KEY":       apiKey,
	} {
		if v == "" {
			return fmt.Errorf("missing required environment variable %s", name)
		}
	}

	owner, repo, ok := strings.Cut(repoFull, "/")
	if !ok {
		return fmt.Errorf("GITHUB_REPOSITORY %q is not in owner/repo form", repoFull)
	}
	// A plain PAT for now -- see internal/githubauth and this file's own
	// doc comment for the GitHub App path this is meant to grow into.
	client := ghclient.New(githubToken, owner, repo)

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
		Client:      client,
		Repo:        repoFull,
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
