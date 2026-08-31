// Package orchestrate holds the pull_request-event handling logic that
// used to live directly in cmd/agent/main.go (package main), extracted
// so a second trigger source -- a future GitHub App webhook, see
// internal/webhook -- can call the exact same functions instead of a
// second, parallel implementation. Nothing in this package's behavior
// changed in the extraction; it is the same gather -> assess -> post ->
// notify sequence cmd/agent has always run for a pull_request event.
package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/dimension/ai-ci-agent/internal/assess"
	"github.com/dimension/ai-ci-agent/internal/gather"
	"github.com/dimension/ai-ci-agent/internal/ghclient"
	"github.com/dimension/ai-ci-agent/internal/notify"
	"github.com/dimension/ai-ci-agent/internal/post"
	"github.com/dimension/ai-ci-agent/internal/provider"
)

// PullRequestEvent is the pull_request event payload shape, shared by
// two independent sources: the GITHUB_EVENT_PATH file GitHub Actions
// writes for a pull_request-triggered job step, and the JSON body of a
// real GitHub webhook delivery for the same event type -- the two are
// byte-for-byte the same schema, which is what makes reusing one struct
// (and everything below that consumes it) correct rather than
// coincidental.
type PullRequestEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		HTMLURL string `json:"html_url"`
		Body    string `json:"body"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Merged bool `json:"merged"`
	} `json:"pull_request"`
}

// HandlePullRequestEvent sends a Slack lifecycle notification for a PR
// opened/closed/merged event. "opened" posts the one top-level root
// message for the PR and persists its ts (via SaveThreadRoot) so every
// later event -- CI failure, AI review, closed -- can reply in that same
// thread instead of posting a new top-level message. Findings/diagnosis
// text never reach Slack; they stay in the GitHub PR comment posted by
// ReviewPR/investigate.
//
// Posting the root message is this path's entire purpose for "opened",
// so a failure there is returned as this call's error, same as every
// other caller. "closed"/"merged" are best-effort thread replies: a
// lookup miss or send failure is logged and swallowed rather than
// failing the caller -- there's no comment fallback to protect here, and
// a closing ping is the least essential of the four events.
func HandlePullRequestEvent(ctx context.Context, client *ghclient.Client, event *PullRequestEvent, repo string, slackCfg notify.SlackConfig) error {
	pr := event.PullRequest
	info := notify.PullRequest{
		Number:  pr.Number,
		Title:   pr.Title,
		HTMLURL: pr.HTMLURL,
		Author:  pr.User.Login,
		Repo:    repo,
		BaseRef: pr.Base.Ref,
		HeadSHA: pr.Head.SHA,
		Body:    pr.Body,
	}

	switch {
	case event.Action == "opened":
		ts, err := notify.Post(ctx, slackCfg, notify.RenderOpened(info), "")
		if err != nil {
			return fmt.Errorf("notify: %w", err)
		}
		if ts != "" { // slackCfg was enabled and the send succeeded
			if err := notify.SaveThreadRoot(ctx, client, pr.Number, ts); err != nil {
				log.Printf("warning: could not save slack thread root for pr %d: %v", pr.Number, err)
			}
		}
		return nil

	case event.Action == "closed" && !pr.Merged:
		ReplyInThread(ctx, client, slackCfg, pr.Number, notify.RenderClosed(info))
		return nil

	case event.Action == "closed" && pr.Merged:
		ReplyInThread(ctx, client, slackCfg, pr.Number, notify.RenderMerged(info))
		return nil

	default:
		log.Printf("pull_request action %q: nothing to notify", event.Action)
		return nil
	}
}

// ReplyInThread posts msg as a reply under prNumber's saved Slack thread
// root, best-effort: a disabled slackCfg, a missing/unlookupable root,
// or a send failure are all logged and swallowed rather than raised,
// since every caller treats its own Slack reply as optional. Exported so
// cmd/agent's still-in-place investigate() (CI-failure path) and this
// package's own ReviewPR/HandlePullRequestEvent share one implementation.
func ReplyInThread(ctx context.Context, client *ghclient.Client, slackCfg notify.SlackConfig, prNumber int, msg notify.SlackAttachmentMessage) {
	if !slackCfg.Enabled() {
		return
	}
	ts, err := notify.FindThreadRoot(ctx, client, prNumber)
	if err != nil {
		log.Printf("warning: could not look up slack thread for pr %d: %v", prNumber, err)
		return
	}
	if ts == "" {
		log.Printf("pr %d: no slack thread root found, skipping reply", prNumber)
		return
	}
	if _, err := notify.Post(ctx, slackCfg, msg, ts); err != nil {
		log.Printf("warning: slack thread reply failed for pr %d: %v", prNumber, err)
	}
}

// ReviewPR runs a plain PR code review -- independent of any CI outcome
// -- for the pull_request opened/synchronize (and, via the webhook path,
// reopened) trigger. It mirrors the CI-failure investigate() path's
// gather -> assess -> post shape and the §7 degrade-gracefully rules,
// scaled down since there's no CI run to fall back to. Posting uses
// PostReview/reviewMarker, a distinct marker from investigate()'s, so a
// review comment and a CI-failure comment on the same commit SHA never
// collide in the idempotency lookup.
//
// A freshly posted (non-degraded, non-already-posted) review also gets
// a Slack "AI Review" reply under the PR's thread root. Degrade cases
// (assessErr != nil) have no structured ReviewResult to summarize, so
// they get no Slack reply at all, only the GitHub fallback comment.
func ReviewPR(ctx context.Context, client *ghclient.Client, llmProvider provider.Provider, prNumber int, headSHA string, slackCfg notify.SlackConfig) error {
	req, mergeState, gatherErr := gather.GatherForReview(ctx, client, prNumber)

	var result provider.ReviewResult
	assessErr := gatherErr
	if gatherErr == nil {
		result, assessErr = llmProvider.Review(ctx, req)
	}

	summary, comments := renderReviewSummary(assessErr, result, headSHA, mergeState.Conflicting())

	url, alreadyPosted, err := post.PostReview(ctx, client, prNumber, headSHA, summary, comments)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	if alreadyPosted {
		log.Printf("pr %d: review already posted: %s", prNumber, url)
		return nil
	}

	log.Printf("pr %d: posted review: %s", prNumber, url)
	if assessErr == nil {
		findings, recommendations := reviewFindingsForSlack(result.Findings)
		msg := notify.RenderAIReview(notify.PullRequest{Number: prNumber}, result.Summary, findings, recommendations)
		ReplyInThread(ctx, client, slackCfg, prNumber, msg)
	}
	return nil
}

// reviewFindingsForSlack translates provider.Assessment findings into
// the plain-string findings/recommendations RenderAIReview renders,
// keeping the notify package independent of the review/assessment
// domain types. findings gets one "location — comment" line per
// assessment; recommendations gets each non-empty SuggestedFix.
func reviewFindingsForSlack(assessments []provider.Assessment) (findings, recommendations []string) {
	for _, a := range assessments {
		loc := a.Category
		if a.File != "" {
			loc = fmt.Sprintf("`%s:%d` %s", a.File, a.Line, a.Category)
		}
		comment := strings.TrimSpace(a.Comment)
		if comment == "" {
			comment = "_none provided_"
		}
		findings = append(findings, fmt.Sprintf("%s — %s", loc, comment))

		if fix := strings.TrimSpace(a.SuggestedFix); fix != "" {
			recommendations = append(recommendations, fix)
		}
	}
	return findings, recommendations
}

// renderReviewSummary is renderReview's (cmd/agent/main.go) counterpart
// for the review path: the same §7 degrade-gracefully cases, minus the
// fallback's raw-logs link (there's no CI run here to link to).
// conflicting is passed through to the success path only -- the degrade
// cases already show a generic fallback message and have no structured
// review to annotate.
func renderReviewSummary(assessErr error, result provider.ReviewResult, headSHA string, conflicting bool) (string, []post.ReviewComment) {
	switch {
	case assessErr == nil:
		return post.RenderReviewReview(result, headSHA, conflicting)

	case errors.Is(assessErr, assess.ErrMalformed):
		log.Printf("review malformed after repair attempt: %v", assessErr)
		return post.RenderReviewMinimal("the model's output could not be parsed as a valid review, even after one repair attempt"), nil

	case IsRateLimited(assessErr):
		log.Printf("rate limited: %v", assessErr)
		return post.RenderReviewMinimal("the GitHub API rate limit was hit while gathering the diff"), nil

	default:
		log.Printf("provider unavailable: %v", assessErr)
		return post.RenderReviewFallback(), nil
	}
}

// IsRateLimited reports whether err is (or wraps) a
// ghclient.RateLimitedError. Exported so cmd/agent's CI-failure path
// (which has its own, unmoved renderReview) and the webhook path share
// one classification instead of two copies of the same three-line
// errors.As check.
func IsRateLimited(err error) bool {
	var rl *ghclient.RateLimitedError
	return errors.As(err, &rl)
}

// ShouldReview reports whether a pull_request action warrants running
// ReviewPR. cmd/agent's own Action-triggered path keeps its historical
// opened/synchronize-only gate inline (see run() in cmd/agent/main.go)
// rather than calling this -- changing that trigger's behavior is out of
// scope here. The webhook path (internal/webhook) uses this, and also
// reviews "reopened", since a webhook delivery has no equivalent
// upstream job re-run to fall back on.
func ShouldReview(action string) bool {
	switch action {
	case "opened", "reopened", "synchronize":
		return true
	default:
		return false
	}
}
