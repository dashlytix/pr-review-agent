package post

import (
	"context"
	"fmt"
	"strings"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
)

// review is a GitHub pull request review, per
// https://docs.github.com/en/rest/pulls/reviews.
type review struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
}

type reviewCommentPayload struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

// reviewRequest is the "create a review" request body. Event is always
// "COMMENT" -- this agent only ever reports findings, it never blocks a
// merge by requesting changes or grants one by approving.
type reviewRequest struct {
	CommitID string                 `json:"commit_id"`
	Body     string                 `json:"body"`
	Event    string                 `json:"event"`
	Comments []reviewCommentPayload `json:"comments,omitempty"`
}

// Exists reports whether a review carrying sha's marker has already been
// posted to the given PR. Used by the reconciliation sweep (§7) to skip
// runs that were already handled by the normal event trigger.
func Exists(ctx context.Context, client *ghclient.Client, prNumber int, sha string) (bool, error) {
	existing, err := findReviewByMarker(ctx, client, prNumber, marker(sha))
	if err != nil {
		return false, err
	}
	return existing != nil, nil
}

// Post posts a GitHub pull request review -- a top-level summary plus one
// inline comment per anchored finding -- after checking for an existing
// review carrying this run's marker, per §6.3's lookup-based idempotency.
// If one is found it is returned as-is rather than duplicated -- this
// covers both a workflow re-run on the same commit and any accidental
// double-invocation.
func Post(ctx context.Context, client *ghclient.Client, prNumber int, sha, summary string, comments []ReviewComment) (postedURL string, alreadyPosted bool, err error) {
	return postReview(ctx, client, prNumber, sha, marker(sha), summary, comments)
}

// PostReview is Post's counterpart for the plain PR-review path (see
// reviewMarker) — kept as a separate marker so a review comment and a
// CI-failure comment on the same commit SHA never collide in the
// idempotency lookup.
func PostReview(ctx context.Context, client *ghclient.Client, prNumber int, sha, summary string, comments []ReviewComment) (postedURL string, alreadyPosted bool, err error) {
	return postReview(ctx, client, prNumber, sha, reviewMarker(sha), summary, comments)
}

func postReview(ctx context.Context, client *ghclient.Client, prNumber int, sha, m, summary string, comments []ReviewComment) (postedURL string, alreadyPosted bool, err error) {
	existing, err := findReviewByMarker(ctx, client, prNumber, m)
	if err != nil {
		return "", false, fmt.Errorf("post: check existing reviews: %w", err)
	}
	if existing != nil {
		return existing.HTMLURL, true, nil
	}

	payload := reviewRequest{CommitID: sha, Body: summary, Event: "COMMENT"}
	for _, c := range comments {
		payload.Comments = append(payload.Comments, reviewCommentPayload{Path: c.Path, Line: c.Line, Side: "RIGHT", Body: c.Body})
	}

	var created review
	if err := client.PostJSON(ctx, client.RepoPath("/pulls/%d/reviews", prNumber), payload, &created); err != nil {
		return "", false, fmt.Errorf("post: create review: %w", err)
	}
	return created.HTMLURL, false, nil
}

// findReviewByMarker mirrors the old issue-comment lookup, but against a
// pull request's reviews -- a distinct GitHub API object with its own
// list endpoint, not a special case of "comments".
func findReviewByMarker(ctx context.Context, client *ghclient.Client, prNumber int, m string) (*review, error) {
	for page := 1; page <= 5; page++ {
		var batch []review
		path := client.RepoPath("/pulls/%d/reviews?per_page=100&page=%d", prNumber, page)
		if err := client.GetJSON(ctx, path, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return nil, nil
		}
		for _, r := range batch {
			if strings.Contains(r.Body, m) {
				return &r, nil
			}
		}
		if len(batch) < 100 {
			return nil, nil
		}
	}
	return nil, nil
}
