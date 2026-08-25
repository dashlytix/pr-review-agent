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
//
// GITHUB_EVENT_NAME=pull_request drives both a Slack lifecycle
// notification (opened/closed/merged) and, independent of any CI
// outcome, a full code review on opened/synchronize (reviewPR) — the two
// are unrelated: every pull_request event gets the Slack ping, and
// opened/synchronize additionally get the review. All Slack notifications
// for one PR -- opened, CI failure, AI review, closed -- share a single
// Slack thread: "opened" posts the one top-level root message and saves
// its ts (via notify.SaveThreadRoot), every later event replies under it
// (via notify.FindThreadRoot + replyInThread), and a missing/unlookupable
// root is handled gracefully rather than falling back to a new top-level
// message.
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
// are enabled, supply the author/URL for the "CI check failed" Slack
// notification — one API call serving both needs.
type pullRequestMeta struct {
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
// plain PR-review path (reviewPR) — Head.SHA and Body are used by both.
type pullRequestEvent struct {
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
	llmProxyAPIKey := os.Getenv("INPUT_LLM-PROXY-API-KEY")
	llmFallbackModel := os.Getenv("INPUT_LLM-FALLBACK-MODEL")
	repoFull := os.Getenv("GITHUB_REPOSITORY")
	eventName := os.Getenv("GITHUB_EVENT_NAME")
	eventPath := os.Getenv("GITHUB_EVENT_PATH")
	slackCfg := notify.SlackConfig{
		BotToken: os.Getenv("INPUT_SLACK-BOT-TOKEN"),
		Channel:  os.Getenv("INPUT_SLACK-CHANNEL"),
	}

	// §7: "Selected provider not configured / missing key — Action fails
	// fast with a clear setup error rather than a silent fallback."
	if token == "" {
		return fmt.Errorf("missing github token (github-token input or GITHUB_TOKEN)")
	}
	owner, repo, ok := strings.Cut(repoFull, "/")
	if !ok {
		return fmt.Errorf("GITHUB_REPOSITORY %q is not in owner/repo form", repoFull)
	}
	client := ghclient.New(token, owner, repo)

	if eventName == "pull_request" {
		event, err := loadPullRequestEvent(eventPath)
		if err != nil {
			return fmt.Errorf("read pull_request event: %w", err)
		}
		if err := handlePullRequestEvent(ctx, client, event, repo, slackCfg); err != nil {
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
		llmProvider, err := buildProvider(providerName, apiKey, llmBaseURL, llmModel, llmProxyAPIKey, llmFallbackModel)
		if err != nil {
			return err
		}
		pr := event.PullRequest
		return reviewPR(ctx, client, llmProvider, pr.Number, pr.Head.SHA, slackCfg)
	}

	// The remaining paths (schedule, workflow_run) drive an LLM
	// assessment, so they need a provider — the pull_request "closed"
	// notification above doesn't and returns before reaching here.
	if apiKey == "" {
		return fmt.Errorf("missing llm-api-key input")
	}
	llmProvider, err := buildProvider(providerName, apiKey, llmBaseURL, llmModel, llmProxyAPIKey, llmFallbackModel)
	if err != nil {
		return err
	}

	if eventName == "schedule" {
		return reconcile(ctx, client, llmProvider, repo, slackCfg)
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

	return investigate(ctx, client, llmProvider, event.WorkflowRun.ID, prNumber, repo, slackCfg)
}

// buildProvider resolves a Provider from action inputs, shared by every
// path that needs one (the workflow_run failure path and the schedule
// reconciliation sweep) so the base-URL/model override precedence lives
// in one place.
func buildProvider(providerName, apiKey, llmBaseURL, llmModel, llmProxyAPIKey, llmFallbackModel string) (provider.Provider, error) {
	llmCfg := provider.ConfigFromEnv(providerName, apiKey)
	if llmBaseURL != "" {
		llmCfg.BaseURL = llmBaseURL
	}
	if llmModel != "" {
		llmCfg.Model = llmModel
	}
	if llmProxyAPIKey != "" {
		llmCfg.ProxyAPIKey = llmProxyAPIKey
	}
	if llmFallbackModel != "" {
		llmCfg.FallbackModel = llmFallbackModel
	}
	return provider.New(llmCfg) // unsupported provider name fails fast here, per §7
}

// handlePullRequestEvent sends a Slack lifecycle notification for a PR
// opened/closed/merged webhook event. "opened" posts the one top-level
// root message for the PR and persists its ts (via SaveThreadRoot) so
// every later event -- CI failure, AI review, closed -- can reply in
// that same thread instead of posting a new top-level message. Findings/
// diagnosis text never reach Slack; they stay in the GitHub PR comment
// posted by reviewPR/investigate.
//
// Posting the root message is this path's entire purpose for "opened",
// so a failure there is returned as this run's error, same as before
// this thread-based rework. "closed"/"merged" are best-effort thread
// replies: per the "handle a missing root message gracefully"
// requirement, a lookup miss or send failure is logged and swallowed
// rather than failing the run -- there's no comment fallback to protect
// here, and a closing ping is the least essential of the four events.
func handlePullRequestEvent(ctx context.Context, client *ghclient.Client, event *pullRequestEvent, repo string, slackCfg notify.SlackConfig) error {
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
		replyInThread(ctx, client, slackCfg, pr.Number, notify.RenderClosed(info))
		return nil

	case event.Action == "closed" && pr.Merged:
		replyInThread(ctx, client, slackCfg, pr.Number, notify.RenderMerged(info))
		return nil

	default:
		log.Printf("pull_request action %q: nothing to notify", event.Action)
		return nil
	}
}

// replyInThread posts msg as a reply under prNumber's saved Slack thread
// root, best-effort: a disabled slackCfg, a missing/unlookupable root,
// or a send failure are all logged and swallowed rather than raised,
// since every caller treats its own Slack reply as optional.
func replyInThread(ctx context.Context, client *ghclient.Client, slackCfg notify.SlackConfig, prNumber int, msg notify.SlackAttachmentMessage) {
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

// investigate runs the full gather → assess → post sequence (§5) for one
// workflow run. Every failure mode past this point (§7) degrades to a
// posted comment rather than a non-zero exit — only the config checks in
// run() are treated as fatal.
func investigate(ctx context.Context, client *ghclient.Client, llmProvider provider.Provider, runID int64, prNumber int, repo string, slackCfg notify.SlackConfig) error {
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
	// "CI check failed" notification's author/URL.
	meta, metaErr := fetchPRMeta(ctx, client, result.PRNumber)
	staleHeadSHA := ""
	if metaErr != nil {
		// Best-effort context for the comment, not a hard dependency —
		// don't let it block posting the assessment itself.
		log.Printf("warning: could not verify current PR head: %v", metaErr)
	} else if meta.Head.SHA != result.HeadSHA {
		staleHeadSHA = meta.Head.SHA
	}

	summary, comments := renderReview(assessErr, findings, result, staleHeadSHA)

	url, alreadyPosted, err := post.Post(ctx, client, result.PRNumber, result.HeadSHA, summary, comments)
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
		// posted, so a reconcile pass never double-pings Slack. Posted as
		// a reply under the PR's Slack thread root (see
		// handlePullRequestEvent's "opened" case), never a new top-level
		// message -- replyInThread degrades gracefully when that root
		// can't be found.
		if metaErr == nil {
			// Degrade cases (assessErr != nil) never produced structured
			// findings to bucket, so the card omits the Impact field
			// entirely rather than claiming a verdict it doesn't have.
			var impactEmoji, impactLabel string
			if assessErr == nil {
				impactEmoji, impactLabel = post.OverallImpact(findings)
			}
			msg := notify.RenderCIFailurePosted(
				notify.PullRequest{Number: result.PRNumber, Repo: repo, HTMLURL: meta.HTMLURL, Author: meta.User.Login},
				impactEmoji, impactLabel,
			)
			replyInThread(ctx, client, slackCfg, result.PRNumber, msg)
		}
	}

	writeOutput("comment-url", url)
	writeOutput("comment-body", summary)
	return nil
}

// reviewPR runs a plain PR code review — independent of any CI outcome —
// for the pull_request opened/synchronize trigger. It mirrors
// investigate()'s gather → assess → post shape and §7 degrade-gracefully
// rules, scaled down since there's no CI run to fall back to. Posting
// uses PostReview/reviewMarker, a distinct marker from investigate()'s,
// so a review comment and a CI-failure comment on the same commit SHA
// never collide in the idempotency lookup.
//
// A freshly posted (non-degraded, non-already-posted) review also gets
// a Slack "AI Review" reply under the PR's thread root, mirroring
// investigate()'s CI-failure notification. Degrade cases (assessErr !=
// nil) have no structured ReviewResult to summarize, so -- same as
// before this thread-based rework -- they get no Slack reply at all,
// only the GitHub fallback comment.
func reviewPR(ctx context.Context, client *ghclient.Client, llmProvider provider.Provider, prNumber int, headSHA string, slackCfg notify.SlackConfig) error {
	req, gatherErr := gather.GatherForReview(ctx, client, prNumber)

	var result provider.ReviewResult
	assessErr := gatherErr
	if gatherErr == nil {
		result, assessErr = llmProvider.Review(ctx, req)
	}

	summary, comments := renderReviewSummary(assessErr, result, headSHA)

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
		replyInThread(ctx, client, slackCfg, prNumber, msg)
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

// renderReviewSummary is renderReview's counterpart for the review path:
// the same §7 degrade-gracefully cases, minus the fallback's raw-logs
// link (there's no CI run here to link to).
func renderReviewSummary(assessErr error, result provider.ReviewResult, headSHA string) (string, []post.ReviewComment) {
	switch {
	case assessErr == nil:
		return post.RenderReviewReview(result, headSHA)

	case errors.Is(assessErr, assess.ErrMalformed):
		log.Printf("review malformed after repair attempt: %v", assessErr)
		return post.RenderReviewMinimal("the model's output could not be parsed as a valid review, even after one repair attempt"), nil

	case isRateLimited(assessErr):
		log.Printf("rate limited: %v", assessErr)
		return post.RenderReviewMinimal("the GitHub API rate limit was hit while gathering the diff"), nil

	default:
		log.Printf("provider unavailable: %v", assessErr)
		return post.RenderReviewFallback(), nil
	}
}

// reconcile is the §7 backstop for a dropped webhook: sweep recent
// failed runs and catch up any still missing a marker comment.
func reconcile(ctx context.Context, client *ghclient.Client, llmProvider provider.Provider, repo string, slackCfg notify.SlackConfig) error {
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
		if err := investigate(ctx, client, llmProvider, r.ID, result.PRNumber, repo, slackCfg); err != nil {
			log.Printf("reconcile: run %d: %v", r.ID, err)
		}
	}
	return nil
}

// renderReview turns the outcome of the Assess call into the review
// summary + inline comments to post, applying §7's degrade-gracefully
// rules. The three degrade cases have no structured findings to anchor,
// so they carry nil comments alongside their plain-string summary.
func renderReview(assessErr error, findings []provider.Assessment, result *gather.Result, staleHeadSHA string) (string, []post.ReviewComment) {
	switch {
	case assessErr == nil:
		return post.RenderAssessmentReview(findings, result.HeadSHA, staleHeadSHA)

	case errors.Is(assessErr, assess.ErrMalformed):
		log.Printf("assessment malformed after repair attempt: %v", assessErr)
		return post.RenderMinimal("the model's output could not be parsed as a valid assessment, even after one repair attempt"), nil

	case isRateLimited(assessErr):
		log.Printf("rate limited: %v", assessErr)
		return post.RenderMinimal("the GitHub API rate limit was hit while gathering context"), nil

	default:
		log.Printf("provider unavailable: %v", assessErr)
		return post.RenderFallback(result.RunHTMLURL), nil
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
