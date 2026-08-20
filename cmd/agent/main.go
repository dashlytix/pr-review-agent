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
	"github.com/dimension/ai-ci-agent/internal/post"
	"github.com/dimension/ai-ci-agent/internal/provider"
)

// pullRequestHead is only used to re-check the PR's current head before
// posting (§6.3 "stale-head aware").
type pullRequestHead struct {
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
	repoFull := os.Getenv("GITHUB_REPOSITORY")
	eventName := os.Getenv("GITHUB_EVENT_NAME")
	eventPath := os.Getenv("GITHUB_EVENT_PATH")

	// §7: "Selected provider not configured / missing key — Action fails
	// fast with a clear setup error rather than a silent fallback."
	if token == "" {
		return fmt.Errorf("missing github token (github-token input or GITHUB_TOKEN)")
	}
	if apiKey == "" {
		return fmt.Errorf("missing llm-api-key input")
	}
	owner, repo, ok := strings.Cut(repoFull, "/")
	if !ok {
		return fmt.Errorf("GITHUB_REPOSITORY %q is not in owner/repo form", repoFull)
	}

	llmProvider, err := provider.Get(providerName, apiKey)
	if err != nil {
		return err // unsupported provider name — fail fast, per §7
	}

	client := ghclient.New(token, owner, repo)

	if eventName == "schedule" {
		return reconcile(ctx, client, llmProvider)
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

	return investigate(ctx, client, llmProvider, event.WorkflowRun.ID, prNumber)
}

// investigate runs the full gather → assess → post sequence (§5) for one
// workflow run. Every failure mode past this point (§7) degrades to a
// posted comment rather than a non-zero exit — only the config checks in
// run() are treated as fatal.
func investigate(ctx context.Context, client *ghclient.Client, llmProvider provider.Provider, runID int64, prNumber int) error {
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

	staleHeadSHA, headErr := currentHead(ctx, client, result.PRNumber)
	if headErr != nil {
		// Head-check is best-effort context for the comment, not a hard
		// dependency — don't let it block posting the assessment itself.
		log.Printf("warning: could not verify current PR head: %v", headErr)
		staleHeadSHA = ""
	}
	if staleHeadSHA == result.HeadSHA {
		staleHeadSHA = "" // not stale
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
	}

	writeOutput("comment-url", url)
	writeOutput("comment-body", body)
	return nil
}

// reconcile is the §7 backstop for a dropped webhook: sweep recent
// failed runs and catch up any still missing a marker comment.
func reconcile(ctx context.Context, client *ghclient.Client, llmProvider provider.Provider) error {
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
		if err := investigate(ctx, client, llmProvider, r.ID, result.PRNumber); err != nil {
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

func currentHead(ctx context.Context, client *ghclient.Client, prNumber int) (string, error) {
	var pr pullRequestHead
	if err := client.GetJSON(ctx, client.RepoPath("/pulls/%d", prNumber), &pr); err != nil {
		return "", err
	}
	return pr.Head.SHA, nil
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
