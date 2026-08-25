// Package notify sends best-effort Slack lifecycle notifications for PR
// opened/CI-check-failed/AI-review/closed events, threaded under one
// root message per PR. It has no GitHub-side idempotency equivalent to
// internal/post's marker comments for the Slack side, so sends are
// single-attempt, no-retry; PR<->thread association is persisted via
// SaveThreadRoot/FindThreadRoot (see thread.go), since the GitHub
// webhook API's own comment/review markers are unrelated to this.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

// chatPostMessageURL is Slack's chat.postMessage Web API endpoint,
// overridable so tests can point it at an httptest.Server instead of
// slack.com.
var chatPostMessageURL = "https://slack.com/api/chat.postMessage"

// SlackConfig holds the credentials chat.postMessage needs. Unlike the
// old incoming-webhook integration, both a bot token (with the
// chat:write scope) and a destination channel are required -- a bare
// webhook URL can't be used here because its response carries no
// message ts, and a ts is what makes thread replies possible at all.
// Either field empty means Slack notifications are disabled.
type SlackConfig struct {
	BotToken string
	Channel  string
}

// Enabled reports whether cfg has enough to post -- callers use this to
// skip GitHub-side thread-lookup calls entirely when Slack isn't wired
// up, not just the Slack call itself.
func (cfg SlackConfig) Enabled() bool {
	return cfg.BotToken != "" && cfg.Channel != ""
}

type chatPostMessageRequest struct {
	Channel     string            `json:"channel"`
	ThreadTS    string            `json:"thread_ts,omitempty"`
	Attachments []SlackAttachment `json:"attachments"`
}

type chatPostMessageResponse struct {
	OK    bool   `json:"ok"`
	TS    string `json:"ts"`
	Error string `json:"error"`
}

// Post sends msg via chat.postMessage, either as a new top-level message
// (threadTS == "") or as a reply in the thread rooted at threadTS. It
// returns the new message's own ts -- callers creating a thread root
// persist that (see SaveThreadRoot) so later events can find it again.
// A disabled cfg is a no-op returning "", nil, so callers don't each
// need their own enabled/disabled check.
func Post(ctx context.Context, cfg SlackConfig, msg SlackAttachmentMessage, threadTS string) (string, error) {
	if !cfg.Enabled() {
		return "", nil
	}

	payload := chatPostMessageRequest{Channel: cfg.Channel, ThreadTS: threadTS, Attachments: msg.Attachments}
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatPostMessageURL, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+cfg.BotToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("notify: slack request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("notify: slack: %d: %s", resp.StatusCode, string(body))
	}

	var out chatPostMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("notify: slack: decode response: %w", err)
	}
	// chat.postMessage always answers 200, even on failure -- the real
	// success/failure signal is the "ok" field, with "error" naming the
	// reason (e.g. "channel_not_found", "not_in_channel").
	if !out.OK {
		return "", fmt.Errorf("notify: slack: %s", out.Error)
	}
	return out.TS, nil
}
