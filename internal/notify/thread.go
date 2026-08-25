package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
)

// threadMarkerPrefix/threadMarkerSuffix hide a PR's Slack thread-root ts
// inside a GitHub issue comment -- the same "idempotent by lookup, not
// by database" idiom internal/post's marker.go uses for its own
// idempotency, applied here to bridge Slack thread state across this
// agent's separate, stateless invocations (one process per GitHub
// event; nothing else persists between them).
const (
	threadMarkerPrefix = "<!-- ai-ci-agent:slack-thread:ts="
	threadMarkerSuffix = " -->"
)

type issueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

// SaveThreadRoot records ts as PR prNumber's Slack thread root by
// posting a small marker-only GitHub issue comment, so a later event
// (CI failure, AI review, closed, ...) can find it via FindThreadRoot
// and reply in-thread instead of posting a new top-level message.
func SaveThreadRoot(ctx context.Context, client *ghclient.Client, prNumber int, ts string) error {
	body := fmt.Sprintf(
		"_pr-review-agent: internal bookkeeping for Slack threading -- safe to ignore._\n%s%s%s",
		threadMarkerPrefix, ts, threadMarkerSuffix,
	)
	return client.PostJSON(ctx, client.RepoPath("/issues/%d/comments", prNumber), map[string]string{"body": body}, nil)
}

// FindThreadRoot looks up PR prNumber's Slack thread-root ts saved by
// SaveThreadRoot, returning "" if none is found -- e.g. Slack was
// disabled (or the "opened" event's send failed) when the PR was
// opened, or the marker comment was deleted. Callers treat that as "no
// thread to reply into" and skip gracefully rather than failing.
func FindThreadRoot(ctx context.Context, client *ghclient.Client, prNumber int) (string, error) {
	for page := 1; page <= 5; page++ {
		var batch []issueComment
		path := client.RepoPath("/issues/%d/comments?per_page=100&page=%d", prNumber, page)
		if err := client.GetJSON(ctx, path, &batch); err != nil {
			return "", err
		}
		if len(batch) == 0 {
			return "", nil
		}
		for _, c := range batch {
			if ts := extractThreadTS(c.Body); ts != "" {
				return ts, nil
			}
		}
		if len(batch) < 100 {
			return "", nil
		}
	}
	return "", nil
}

func extractThreadTS(body string) string {
	start := strings.Index(body, threadMarkerPrefix)
	if start < 0 {
		return ""
	}
	rest := body[start+len(threadMarkerPrefix):]
	end := strings.Index(rest, threadMarkerSuffix)
	if end < 0 {
		return ""
	}
	return rest[:end]
}
