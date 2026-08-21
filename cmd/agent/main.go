// Command agent is the entrypoint for the ai-ci-agent GitHub Action.
// It implements the sequence of operations in §5: gather → assess →
// post → exit, applying the failure-mode handling in §7 at each step so
// a provider outage or a malformed response degrades to a fallback
// comment instead of failing the calling workflow.
//
// Two triggers share the same investigate() logic:
//   - GITHUB_EVENT_NAME=workflow_run: the normal path, invoked as a step
//     in the failing workflow itself.
//   - GITHUB_EVENT_NAME=schedule: the §7 reconciliation backstop for a
//     dropped webhook — sweeps recent failed runs for any missing a
//     marker comment and catches them up.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/dimension/ai-ci-agent/internal/assess"
	"github.com/dimension/ai-ci-agent/internal/gather"
	"github.com/dimension/ai-ci-agent/internal/ghclient"
	"github.com/dimension/ai-ci-agent/internal/notify"
	"github.com/dimension/ai-ci-agent/internal/post"
	"github.com/dimension/ai-ci-agent/internal/provider"
)

// pullRequestMeta is fetched once to both re-check the PR's current head
// before posting (§6.3 "stale-head aware") and, if Slack notifications
// are enabled, supply the title/author/URL for the "review posted"
// notification — one API call serving both needs.
type pullRequestMeta struct {
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

type workflowRunEvent struct {
	WorkflowRun struct {
		ID           int64  `json:"id"`
		Conclusion   string `json:"conclusion"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"workflow_run"`
}

// pullRequestEvent is the GITHUB_EVENT_PATH payload for
// GITHUB_EVENT_NAME=pull_request. It drives both the Slack lifecycle
// notifications (opened/closed/merged) and, on opened/synchronize, the
// plain PR-review path (reviewPR) — Head.SHA is only needed by the
// latter.
type pullRequestEvent struct {
	Action      string `json:"action"`
	PullRequest struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
		Merged             bool `json:"merged"`
		RequestedReviewers []struct {
			Login string `json:"login"`
		} `json:"requested_reviewers"`
	} `json:"pull_request"`
}

// reconcileRunLimit bounds how many recent failed runs the schedule
// trigger inspects per sweep; a ~30 minute cron only needs to look back
// far enough to catch a single dropped webhook, not the whole history.
const reconcileRunLimit = 20

func main() {
	if err := run(); err != nil {
		log.Fatalf("ai-ci-agent: %v", err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), stepTimeout())
	defer cancel()

	token := firstNonEmpty(os.Getenv("INPUT_GITHUB-TOKEN"), os.Getenv("GITHUB_TOKEN"))
	providerName := envOr("INPUT_LLM-PROVIDER", "claude")
	apiKey := os.Getenv("INPUT_LLM-API-KEY")
	// Inputs win over the environment: a workflow that spells the gateway
	// out in action.yml shouldn't be silently overridden by an ambient
	// ANTHROPIC_BASE_URL on the runner. Falling back to ConfigFromEnv
	// keeps the eval harness and local shell runs working unchanged.
	llmBaseURL := os.Getenv("INPUT_LLM-BASE-URL")
	llmModel := os.Getenv("INPUT_LLM-MODEL")
	repoFull := os.Getenv("GITHUB_REPOSITORY")
	eventName := os.Getenv("GITHUB_EVENT_NAME")
	eventPath := os.Getenv("GITHUB_EVENT_PATH")
	slackWebhookURL := os.Getenv("INPUT_SLACK-WEBHOOK-URL")
	prodBranch := envOr("INPUT_PROD-BRANCH", "main")

	// §7: "Selected provider not configured / missing key — Action fails
	// fast with a clear setup error rather than a silent fallback."
	if token == "" {
		return fmt.Errorf("missing github token (github-token input or GITHUB_TOKEN)")
	}
	owner, repo, ok := strings.Cut(repoFull, "/")
	if !ok {
		return fmt.Errorf("GITHUB_REPOSITORY %q is not in owner/repo form", repoFull)
	}

	if eventName == "pull_request" {
		event, err := loadPullRequestEvent(eventPath)
		if err != nil {
			return fmt.Errorf("read pull_request event: %w", err)
		}
		if err := handlePullRequestEvent(ctx, event, slackWebhookURL, prodBranch); err != nil {
			return err
		}

		// opened/synchronize also get a full code review — closed and any
		// other action stop here, same as handlePullRequestEvent's own
		// no-op default for actions it doesn't send a Slack ping for.
		if event.Action != "opened" && event.Action != "synchronize" {
			return nil
		}
		// The review path needs a provider, same fail-fast as the
		// schedule/workflow_run paths below; the notify-only actions
		// (closed) returned above without ever needing one.
		if apiKey == "" {
			return fmt.Errorf("missing llm-api-key input")
		}
		llmProvider, err := buildProvider(providerName, apiKey, llmBaseURL, llmModel)
		if err != nil {
			return err
		}
		client := ghclient.New(token, owner, repo)
		pr := event.PullRequest
		return reviewPR(ctx, client, llmProvider, pr.Number, pr.HTMLURL, pr.Title, pr.Head.SHA, slackWebhookURL)
	}

	// The remaining paths (schedule, workflow_run) drive an LLM
	// assessment, so they need a provider — the pull_request "closed"
	// notification above doesn't and returns before reaching here.
	if apiKey == "" {
		return fmt.Errorf("missing llm-api-key input")
	}
	llmProvider, err := buildProvider(providerName, apiKey, llmBaseURL, llmModel)
	if err != nil {
		return err
	}

	client := ghclient.New(token, owner, repo)

	if eventName == "schedule" {
		return reconcile(ctx, client, llmProvider, slackWebhookURL)
	}

	event, err := loadWorkflowRunEvent(eventPath)
	if err != nil {
		return fmt.Errorf("read workflow_run event: %w", err)
	}
	if event.WorkflowRun.Conclusion != "failure" {
		log.Printf("workflow run concluded %q, nothing to investigate", event.WorkflowRun.Conclusion)
		return nil
	}

	var prNumber int
	if len(event.WorkflowRun.PullRequests) > 0 {
		prNumber = event.WorkflowRun.PullRequests[0].Number
	}

	return investigate(ctx, client, llmProvider, event.WorkflowRun.ID, prNumber, slackWebhookURL)
}

// buildProvider resolves a Provider from action inputs, shared by every
// path that needs one (workflow_run, schedule, and the pull_request
// review path) so the base-URL/model override precedence lives in one
// place.
func buildProvider(providerName, apiKey, llmBaseURL, llmModel string) (provider.Provider, error) {
	llmCfg := provider.ConfigFromEnv(providerName, apiKey)
	if llmBaseURL != "" {
		llmCfg.BaseURL = llmBaseURL
	}
	if llmModel != "" {
		llmCfg.Model = llmModel
	}
	return provider.New(llmCfg) // unsupported provider name fails fast here, per §7
}

// handlePullRequestEvent sends a Slack notification for a PR
// opened/closed/merged webhook event. Sending is the entire purpose of
// this path (there's no PR comment fallback here), so a send failure is
// returned as this run's error rather than merely logged.
func handlePullRequestEvent(ctx context.Context, event *pullRequestEvent, slackWebhookURL, prodBranch string) error {
	pr := event.PullRequest

	reviewers := make([]string, 0, len(pr.RequestedReviewers))
	for _, r := range pr.RequestedReviewers {
		reviewers = append(reviewers, r.Login)
	}
	info := notify.PullRequest{
		Number:    pr.Number,
		Title:     pr.Title,
		HTMLURL:   pr.HTMLURL,
		Author:    pr.User.Login,
		Body:      pr.Body,
		Reviewers: reviewers,
	}

	var text string
	switch {
	case event.Action == "opened":
		text = notify.RenderOpened(info)
	case event.Action == "closed" && !pr.Merged:
		text = notify.RenderClosed(info)
	case event.Action == "closed" && pr.Merged:
		text = notify.RenderMerged(info, pr.Base.Ref, prodBranch)
	default:
		log.Printf("pull_request action %q: nothing to notify", event.Action)
		return nil
	}

	if err := notify.Send(ctx, slackWebhookURL, text); err != nil {
		return fmt.Errorf("notify: %w", err)
	}
	return nil
}

// investigate runs the full gather → assess → post sequence (§5) for one
// workflow run. Every failure mode past this point (§7) degrades to a
// posted comment rather than a non-zero exit — only the config checks in
// run() are treated as fatal.
func investigate(ctx context.Context, client *ghclient.Client, llmProvider provider.Provider, runID int64, prNumber int, slackWebhookURL string) error {
	result, gatherErr := gather.Gather(ctx, client, runID, prNumber)
	if result == nil {
		if gatherErr == nil {
			log.Printf("no pull request associated with run %d, nothing to investigate", runID)
			return nil
		}
		return gatherErr // gather.Gather's own errors are already "gather: ..." prefixed
	}

	// A partial gather failure (e.g. rate limited fetching the diff)
	// still carries enough (PRNumber, HeadSHA) to post a fallback
	// comment, so it's threaded through renderBody below rather than
	// treated as fatal. Assess is skipped in that case — there's no
	// point prompting the model with an incomplete AssessmentRequest.
	var findings []provider.Assessment
	assessErr := gatherErr
	if gatherErr == nil {
		findings, assessErr = llmProvider.Assess(ctx, result.Request)
	}

	// One fetch serves both the stale-head check and (below) the Slack
	// "review posted" notification's title/author/URL.
	meta, metaErr := fetchPRMeta(ctx, client, result.PRNumber)
	staleHeadSHA := ""
	if metaErr != nil {
		// Best-effort context for the comment, not a hard dependency —
		// don't let it block posting the assessment itself.
		log.Printf("warning: could not verify current PR head: %v", metaErr)
	} else if meta.Head.SHA != result.HeadSHA {
		staleHeadSHA = meta.Head.SHA
	}

	body := renderBody(assessErr, findings, result, staleHeadSHA)

	url, alreadyPosted, err := post.Post(ctx, client, result.PRNumber, result.HeadSHA, body)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	if alreadyPosted {
		log.Printf("run %d: assessment already posted: %s", runID, url)
	} else {
		log.Printf("run %d: posted assessment: %s", runID, url)
		// Notification is secondary to the comment that just posted
		// successfully, so a Slack failure here is logged, not fatal —
		// and skipped (with a re-post never re-attempted) once already
		// posted, so a reconcile pass never double-pings Slack.
		if metaErr == nil {
			if err := notify.Send(ctx, slackWebhookURL, notify.RenderAssessmentPosted(meta.Title, meta.HTMLURL, url)); err != nil {
				log.Printf("warning: slack notification failed: %v", err)
			}
		}
	}

	writeOutput("comment-url", url)
	writeOutput("comment-body", body)
	return nil
}

// reviewPR runs a plain PR code review — independent of any CI outcome —
// for the pull_request opened/synchronize trigger. It mirrors
// investigate()'s gather → assess → post shape and §7 degrade-gracefully
// rules, scaled down since there's no CI run to fall back to. Posting
// uses PostReview/reviewMarker, a distinct marker from investigate()'s,
// so a review comment and a CI-failure comment on the same commit SHA
// never collide in the idempotency lookup.
func reviewPR(ctx context.Context, client *ghclient.Client, llmProvider provider.Provider, prNumber int, htmlURL, title, headSHA, slackWebhookURL string) error {
	req, gatherErr := gather.GatherForReview(ctx, client, prNumber)

	var findings []provider.Assessment
	assessErr := gatherErr
	if gatherErr == nil {
		findings, assessErr = llmProvider.Review(ctx, req)
	}

	body := renderReviewBody(assessErr, findings, headSHA)

	url, alreadyPosted, err := post.PostReview(ctx, client, prNumber, headSHA, body)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	if alreadyPosted {
		log.Printf("pr %d: review already posted: %s", prNumber, url)
		return nil
	}

	log.Printf("pr %d: posted review: %s", prNumber, url)
	if err := notify.Send(ctx, slackWebhookURL, notify.RenderAssessmentPosted(title, htmlURL, url)); err != nil {
		log.Printf("warning: slack notification failed: %v", err)
	}
	return nil
}

// renderReviewBody is renderBody's counterpart for the review path: the
// same §7 degrade-gracefully cases, minus the fallback's raw-logs link
// (there's no CI run here to link to).
func renderReviewBody(assessErr error, findings []provider.Assessment, headSHA string) string {
	switch {
	case assessErr == nil:
		return post.RenderReviewFindings(findings, headSHA)

	case errors.Is(assessErr, assess.ErrMalformed):
		log.Printf("review malformed after repair attempt: %v", assessErr)
		return post.RenderReviewMinimal("the model's output could not be parsed as a valid review, even after one repair attempt", headSHA)

	case isRateLimited(assessErr):
		log.Printf("rate limited: %v", assessErr)
		return post.RenderReviewMinimal("the GitHub API rate limit was hit while gathering the diff", headSHA)

	default:
		log.Printf("provider unavailable: %v", assessErr)
		return post.RenderReviewFallback(headSHA)
	}
}

// reconcile is the §7 backstop for a dropped webhook: sweep recent
// failed runs and catch up any still missing a marker comment.
func reconcile(ctx context.Context, client *ghclient.Client, llmProvider provider.Provider, slackWebhookURL string) error {
	runs, err := gather.RecentFailedRuns(ctx, client, reconcileRunLimit)
	if err != nil {
		return fmt.Errorf("reconcile: list recent failed runs: %w", err)
	}

	for _, r := range runs {
		result, gatherErr := gather.Gather(ctx, client, r.ID, 0)
		if result == nil {
			if gatherErr != nil {
				log.Printf("reconcile: run %d: %v", r.ID, gatherErr)
			}
			continue
		}

		exists, err := post.Exists(ctx, client, result.PRNumber, result.HeadSHA)
		if err != nil {
			log.Printf("reconcile: run %d: check existing comment: %v", r.ID, err)
			continue
		}
		if exists {
			continue // already handled by the normal event trigger
		}

		log.Printf("reconcile: run %d has no marker comment yet, catching up", r.ID)
		if err := investigate(ctx, client, llmProvider, r.ID, result.PRNumber, slackWebhookURL); err != nil {
			log.Printf("reconcile: run %d: %v", r.ID, err)
		}
	}
	return nil
}

// renderBody turns the outcome of the Assess call into the comment body
// to post, applying §7's degrade-gracefully rules.
func renderBody(assessErr error, findings []provider.Assessment, result *gather.Result, staleHeadSHA string) string {
	switch {
	case assessErr == nil:
		return post.RenderAssessments(findings, result.HeadSHA, staleHeadSHA)

	case errors.Is(assessErr, assess.ErrMalformed):
		log.Printf("assessment malformed after repair attempt: %v", assessErr)
		return post.RenderMinimal("the model's output could not be parsed as a valid assessment, even after one repair attempt", result.HeadSHA)

	case isRateLimited(assessErr):
		log.Printf("rate limited: %v", assessErr)
		return post.RenderMinimal("the GitHub API rate limit was hit while gathering context", result.HeadSHA)

	default:
		log.Printf("provider unavailable: %v", assessErr)
		return post.RenderFallback(result.RunHTMLURL, result.HeadSHA)
	}
}

func isRateLimited(err error) bool {
	var rl *ghclient.RateLimitedError
	return errors.As(err, &rl)
}

func fetchPRMeta(ctx context.Context, client *ghclient.Client, prNumber int) (*pullRequestMeta, error) {
	var pr pullRequestMeta
	if err := client.GetJSON(ctx, client.RepoPath("/pulls/%d", prNumber), &pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

func loadPullRequestEvent(path string) (*pullRequestEvent, error) {
	if path == "" {
		return nil, fmt.Errorf("GITHUB_EVENT_PATH not set")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var event pullRequestEvent
	if err := json.NewDecoder(f).Decode(&event); err != nil {
		return nil, err
	}
	return &event, nil
}

func loadWorkflowRunEvent(path string) (*workflowRunEvent, error) {
	if path == "" {
		return nil, fmt.Errorf("GITHUB_EVENT_PATH not set")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var event workflowRunEvent
	if err := json.NewDecoder(f).Decode(&event); err != nil {
		return nil, err
	}
	return &event, nil
}

func stepTimeout() time.Duration {
	if raw := os.Getenv("AI_CI_AGENT_TIMEOUT_SECONDS"); raw != "" {
		if d, err := time.ParseDuration(raw + "s"); err == nil {
			return d
		}
	}
	return 4 * time.Minute
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// writeOutput sets a GitHub Actions step output via the $GITHUB_OUTPUT
// file protocol, using the multiline-safe delimiter form since
// comment-body is a full markdown comment, not a single-line value. A
// no-op outside a real Actions run (e.g. local testing), where
// GITHUB_OUTPUT isn't set — outputs are a convenience for the calling
// workflow (e.g. forwarding to a chat notification), not something the
// action's own behavior depends on.
func writeOutput(name, value string) {
	path := os.Getenv("GITHUB_OUTPUT")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("warning: could not write output %q: %v", name, err)
		return
	}
	defer f.Close()

	delim := randomDelimiter()
	fmt.Fprintf(f, "%s<<%s\n%s\n%s\n", name, delim, value, delim)
}

func randomDelimiter() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "ghadelim_fallback"
	}
	return "ghadelim_" + hex.EncodeToString(b)
}
