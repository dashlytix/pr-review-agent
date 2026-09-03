package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/dimension/ai-ci-agent/internal/orchestrate"
)

// EventHandler processes one webhook delivery's raw JSON payload for a
// specific X-GitHub-Event type. ctx carries no deadline tied to the
// originating HTTP request -- handlers run after Handler.ServeHTTP has
// already acknowledged the delivery (see its doc comment).
//
// Adding support for a new GitHub event is one new entry in
// eventHandlers, not a change to Handler.ServeHTTP's dispatch logic.
type EventHandler func(ctx context.Context, h *Handler, payload []byte) error

// eventHandlers maps X-GitHub-Event header values to their handler.
var eventHandlers = map[string]EventHandler{
	"pull_request": handlePullRequestWebhook,
}

// handlePullRequestWebhook decodes a pull_request webhook delivery and
// calls into internal/orchestrate -- the exact same functions cmd/agent
// calls for the Actions-triggered pull_request path -- rather than a
// second implementation of PR gathering/LLM assessment/review posting.
//
// GitHub's real pull_request webhook payload and the pull_request event
// file GitHub Actions writes to GITHUB_EVENT_PATH are the same schema
// (Actions' event files are, in effect, the webhook payload for the
// triggering event), which is what makes decoding straight into
// orchestrate.PullRequestEvent correct here.
func handlePullRequestWebhook(ctx context.Context, h *Handler, payload []byte) error {
	var event orchestrate.PullRequestEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return fmt.Errorf("webhook: decode pull_request payload: %w", err)
	}

	// The repository comes from each delivery's own payload, not a
	// Handler-wide fixed value -- this is what lets one deployment serve
	// webhooks registered on any number of repositories, per repository
	// full_name.
	repo := event.Repository.FullName
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return fmt.Errorf("webhook: pull_request payload carried no usable repository.full_name (got %q)", repo)
	}
	client := h.client(owner, name)

	if err := orchestrate.HandlePullRequestEvent(ctx, client, &event, repo, h.SlackConfig); err != nil {
		// Best-effort, matching cmd/agent's own treatment of this call
		// for actions the webhook path doesn't itself gate on below --
		// a Slack notification failure must not block the review.
		log.Printf("webhook: %s pull_request %d: notify failed: %v", repo, event.PullRequest.Number, err)
	}

	// Initially support opened/reopened/synchronize (§Phase 4). Unlike
	// cmd/agent's Action-triggered path, "reopened" is included here --
	// see orchestrate.ShouldReview's doc comment for why the two gates
	// deliberately differ.
	if !orchestrate.ShouldReview(event.Action) {
		log.Printf("webhook: %s pull_request %d action %q: no review triggered", repo, event.PullRequest.Number, event.Action)
		return nil
	}

	pr := event.PullRequest
	if err := orchestrate.ReviewPR(ctx, client, h.Provider, pr.Number, pr.Head.SHA, h.SlackConfig); err != nil {
		return fmt.Errorf("webhook: %s review pr %d: %w", repo, pr.Number, err)
	}
	return nil
}
