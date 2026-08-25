// Command slackbot is the always-on counterpart to cmd/agent: where the
// GitHub Action runs briefly per event and exits, this connects to Slack
// over Socket Mode and stays connected, answering @-mentions inside a
// PR's existing Slack thread (see internal/notify) using that PR's diff.
//
// It is not built by the existing Dockerfile/action.yml -- that
// packaging model is "start a container, run once, exit," which doesn't
// fit an always-on connection. Deploy this as a plain systemd service
// running the compiled binary directly (see deploy/).
//
// Usage:
//
//	SLACK_BOT_TOKEN=xoxb-... SLACK_APP_TOKEN=xapp-... SLACK_CHANNEL=C... \
//	GITHUB_TOKEN=ghp_... GITHUB_REPOSITORY=owner/repo \
//	LLM_PROVIDER=claude LLM_API_KEY=sk-... \
//	go run ./cmd/slackbot
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
	"github.com/dimension/ai-ci-agent/internal/provider"
	"github.com/dimension/ai-ci-agent/internal/slackbot"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("slackbot: %v", err)
	}
}

func run() error {
	botToken := os.Getenv("SLACK_BOT_TOKEN")
	appToken := os.Getenv("SLACK_APP_TOKEN")
	channel := os.Getenv("SLACK_CHANNEL")
	githubToken := os.Getenv("GITHUB_TOKEN")
	repoFull := os.Getenv("GITHUB_REPOSITORY")
	providerName := envOr("LLM_PROVIDER", "claude")
	apiKey := os.Getenv("LLM_API_KEY")

	for name, v := range map[string]string{
		"SLACK_BOT_TOKEN":   botToken,
		"SLACK_APP_TOKEN":   appToken,
		"SLACK_CHANNEL":     channel,
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
	// GITHUB_TOKEN here only ever needs read access -- this daemon reads
	// diffs/PR lists/marker comments and posts to Slack, it never writes
	// to GitHub. A fine-grained read-only PAT is enough; it doesn't need
	// the broader token an Action step gets.
	client := ghclient.New(githubToken, owner, repo)

	llmProvider, err := provider.Get(providerName, apiKey)
	if err != nil {
		return err
	}

	cfg := slackbot.Config{
		BotToken: botToken,
		AppToken: appToken,
		Channel:  channel,
		Client:   client,
		Provider: llmProvider,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	err = slackbot.Run(ctx, cfg)
	if err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
