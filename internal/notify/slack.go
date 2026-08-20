// Package notify sends best-effort Slack notifications for PR lifecycle
// events (opened/closed/merged) and for the AI review comment being
// posted. It has no GitHub-side idempotency equivalent to internal/post's
// marker comments, so sends are single-attempt, no-retry.
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

// Send posts text to a Slack incoming webhook. An empty webhookURL is
// treated as "Slack notifications disabled" and is a no-op, so callers
// don't each need their own enabled/disabled check.
func Send(ctx context.Context, webhookURL, text string) error {
	if webhookURL == "" {
		return nil
	}

	b, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("notify: slack request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		// Slack's webhook endpoint returns a plain-text error body (e.g.
		// "invalid_payload"), not JSON, so it's surfaced as-is rather than
		// decoded.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("notify: slack: %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
