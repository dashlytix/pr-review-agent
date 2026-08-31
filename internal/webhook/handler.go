package webhook

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
	"github.com/dimension/ai-ci-agent/internal/notify"
	"github.com/dimension/ai-ci-agent/internal/provider"
)

// maxBodyBytes bounds how much of a webhook request body is read before
// giving up. GitHub's own webhook payloads are JSON event metadata, not
// diff/file content (the review pipeline fetches those separately via
// the REST API in internal/gather), so even a very large PR's payload is
// normally well under a megabyte; this cap is generous headroom against
// that, not a tight fit to the expected size.
const maxBodyBytes = 5 << 20 // 5 MiB

// Handler holds everything a webhook delivery needs to be validated and
// (for a supported event) turned into a call into internal/orchestrate.
// It deliberately holds no HTTP-specific state -- see Server for that --
// so it can be constructed and unit-tested independent of an actual
// listening socket.
type Handler struct {
	// Secret verifies X-Hub-Signature-256 (see VerifySignature).
	Secret string
	// Idempotency records processed delivery IDs. Defaults to an
	// InMemoryIdempotencyStore if nil -- see that type's doc comment for
	// why that default is development-only.
	Idempotency IdempotencyStore

	// Client is used for every GitHub API call the review pipeline
	// makes. Its Token/TokenSource determines whether this deployment
	// authenticates as a static PAT or (once available) a GitHub App
	// installation -- see internal/githubauth.
	Client *ghclient.Client
	// Repo is "owner/repo", passed through to
	// orchestrate.HandlePullRequestEvent for its Slack notification text.
	Repo string
	// Provider drives the LLM review call.
	Provider provider.Provider
	// SlackConfig is passed straight to internal/orchestrate; a zero
	// value disables Slack notifications, same as every other entrypoint.
	SlackConfig notify.SlackConfig
}

func (h *Handler) idempotency() IdempotencyStore {
	if h.Idempotency != nil {
		return h.Idempotency
	}
	h.Idempotency = NewInMemoryIdempotencyStore()
	return h.Idempotency
}

// ServeHTTP implements the POST /webhooks/github endpoint: read the body
// (size-bounded), verify its signature, check/record idempotency by
// delivery ID, and dispatch to the registered handler for the
// X-GitHub-Event type -- then return immediately. The actual review
// pipeline (internal/orchestrate, which calls out to GitHub/the LLM
// provider/Slack) runs in a detached goroutine after the response is
// sent: GitHub considers a delivery failed if it doesn't get a response
// within about 10 seconds, and this repo's own review calls can already
// take well past that (see the 180s HTTP client timeout and 4-minute
// step budget documented in internal/provider and cmd/agent). This
// mirrors internal/slackbot's own "ack first, process in a goroutine"
// pattern (see daemon.go) for the same underlying reason.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	sig := r.Header.Get("X-Hub-Signature-256")
	if !VerifySignature(h.Secret, body, sig) {
		// Deliberately the same response regardless of *why* the
		// signature didn't verify (missing vs. wrong) -- see
		// VerifySignature's doc comment. The webhook secret and request
		// body are never logged here, only the fact that verification
		// failed.
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	deliveryID := r.Header.Get("X-GitHub-Delivery")
	if deliveryID == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	eventType := r.Header.Get("X-GitHub-Event")

	alreadySeen, err := h.idempotency().CheckAndMark(r.Context(), deliveryID)
	if err != nil {
		log.Printf("webhook: idempotency check failed for delivery %s: %v", deliveryID, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if alreadySeen {
		log.Printf("webhook: delivery %s already processed, acking without reprocessing", deliveryID)
		w.WriteHeader(http.StatusOK)
		return
	}

	handle, ok := eventHandlers[eventType]
	if !ok {
		log.Printf("webhook: delivery %s: unsupported event type %q, ignoring", deliveryID, eventType)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Detached from the request's context (which is cancelled the
	// instant this handler returns) but not from the process lifetime --
	// Server.Shutdown does not wait for these to finish; see its doc
	// comment.
	go func() {
		if err := handle(context.Background(), h, body); err != nil {
			log.Printf("webhook: delivery %s (%s): %v", deliveryID, eventType, err)
		}
	}()

	w.WriteHeader(http.StatusAccepted)
}
