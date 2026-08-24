package post

import (
	"context"
	"fmt"
	"strings"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
)

type issueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
	URL  string `json:"html_url"`
}

// Exists reports whether a comment carrying sha's marker has already
// been posted to the given PR. Used by the reconciliation sweep (§7) to
// skip runs that were already handled by the normal event trigger.
func Exists(ctx context.Context, client *ghclient.Client, prNumber int, sha string) (bool, error) {
	existing, err := findByMarker(ctx, client, prNumber, marker(sha))
	if err != nil {
		return false, err
	}
	return existing != nil, nil
}

// Post checks for an existing comment carrying this run's marker before
// posting, per §6.3's lookup-based idempotency. If one is found it is
// returned as-is rather than duplicated — this covers both a workflow
// re-run on the same commit and any accidental double-invocation.
func Post(ctx context.Context, client *ghclient.Client, prNumber int, sha, body string) (postedURL string, alreadyPosted bool, err error) {
	return postWithMarker(ctx, client, prNumber, marker(sha), body)
}

// PostReview is Post's counterpart for the plain PR-review path (see
// reviewMarker) — kept as a separate marker so a review comment and a
// CI-failure comment on the same commit SHA never collide in the
// idempotency lookup.
func PostReview(ctx context.Context, client *ghclient.Client, prNumber int, sha, body string) (postedURL string, alreadyPosted bool, err error) {
	return postWithMarker(ctx, client, prNumber, reviewMarker(sha), body)
}

// PostPass is Post's counterpart for a passing CI run's short templated
// comment (see passMarker) — its own marker so it never collides with a
// CI-failure or review comment on the same commit SHA.
func PostPass(ctx context.Context, client *ghclient.Client, prNumber int, sha, body string) (postedURL string, alreadyPosted bool, err error) {
	return postWithMarker(ctx, client, prNumber, passMarker(sha), body)
}

func postWithMarker(ctx context.Context, client *ghclient.Client, prNumber int, m, body string) (postedURL string, alreadyPosted bool, err error) {
	existing, err := findByMarker(ctx, client, prNumber, m)
	if err != nil {
		return "", false, fmt.Errorf("post: check existing comments: %w", err)
	}
	if existing != nil {
		return existing.URL, true, nil
	}

	var created issueComment
	err = client.PostJSON(ctx, client.RepoPath("/issues/%d/comments", prNumber), map[string]string{"body": body}, &created)
	if err != nil {
		return "", false, fmt.Errorf("post: create comment: %w", err)
	}
	return created.URL, false, nil
}

func findByMarker(ctx context.Context, client *ghclient.Client, prNumber int, m string) (*issueComment, error) {
	for page := 1; page <= 5; page++ {
		var batch []issueComment
		path := client.RepoPath("/issues/%d/comments?per_page=100&page=%d", prNumber, page)
		if err := client.GetJSON(ctx, path, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return nil, nil
		}
		for _, c := range batch {
			if strings.Contains(c.Body, m) {
				return &c, nil
			}
		}
		if len(batch) < 100 {
			return nil, nil
		}
	}
	return nil, nil
}
