// Package slackbot answers Slack @-mentions inside a PR's Slack thread
// (see internal/notify) using that PR's diff, via an LLM call. Unlike
// the rest of this repo -- a GitHub Action container invoked briefly per
// event, then it exits -- this package backs a genuinely always-on
// process (cmd/slackbot): Slack only pushes events to something actively
// connected via Socket Mode, so there's no "run once, exit" shape here.
package slackbot

import (
	"time"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
	"github.com/dimension/ai-ci-agent/internal/provider"
)

// defaultCacheRefresh is how often the PR-thread-root cache does a full
// sweep of open PRs (see prCache.runRefresh). Conservative on purpose:
// this is a background cost paid regardless of whether anyone mentions
// the bot, and a cache miss still resolves correctly via the lazy
// fallback in prCache.lookup -- it just costs one extra scan instead of
// an in-memory hit.
const defaultCacheRefresh = 10 * time.Minute

// Config carries everything the daemon needs to run. BotToken/AppToken
// are Slack credentials (see cmd/slackbot/main.go for the exact env vars
// each maps to); Channel scopes which Slack channel's mentions are acted
// on, matching the single-channel scope internal/notify already uses.
type Config struct {
	BotToken string
	AppToken string
	Channel  string

	Client   *ghclient.Client
	Provider provider.Provider

	// CacheRefresh overrides defaultCacheRefresh; zero uses the default.
	CacheRefresh time.Duration
}

func (cfg Config) cacheRefresh() time.Duration {
	if cfg.CacheRefresh > 0 {
		return cfg.CacheRefresh
	}
	return defaultCacheRefresh
}
