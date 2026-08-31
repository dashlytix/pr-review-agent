package webhook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
	"github.com/dimension/ai-ci-agent/internal/provider"
)

// fakeProvider is a minimal provider.Provider for tests -- only Review
// is exercised by the pull_request webhook path.
type fakeProvider struct {
	calls int32
}

func (f *fakeProvider) Assess(ctx context.Context, req provider.AssessmentRequest) ([]provider.Assessment, error) {
	panic("Assess should not be called by the webhook's pull_request path")
}
func (f *fakeProvider) Review(ctx context.Context, req provider.AssessmentRequest) (provider.ReviewResult, error) {
	atomic.AddInt32(&f.calls, 1)
	return provider.ReviewResult{Summary: "a fake review"}, nil
}
func (f *fakeProvider) Answer(ctx context.Context, req provider.AssessmentRequest, question string) (string, error) {
	panic("Answer should not be called by the webhook's pull_request path")
}

// testHandler builds a Handler wired against a stub GitHub API server
// (mux) and a fresh in-memory idempotency store, ready for ServeHTTP.
func testHandler(t *testing.T, mux http.Handler, fp provider.Provider) (*Handler, *httptest.Server) {
	t.Helper()
	ghServer := httptest.NewServer(mux)
	t.Cleanup(ghServer.Close)

	client := ghclient.New("test-token", "acme", "widgets")
	client.BaseURL = ghServer.URL
	client.RetryBaseDelay = 5 * time.Millisecond

	return &Handler{
		Secret:      testSecret,
		Idempotency: NewInMemoryIdempotencyStore(),
		Client:      client,
		Repo:        "acme/widgets",
		Provider:    fp,
	}, ghServer
}

func pullRequestOpenedPayload(number int) []byte {
	b, _ := json.Marshal(map[string]any{
		"action": "opened",
		"pull_request": map[string]any{
			"number": number,
			"title":  "test PR",
			"head":   map[string]string{"sha": "deadbeef"},
			"base":   map[string]string{"ref": "main"},
			"user":   map[string]string{"login": "octocat"},
		},
	})
	return b
}

func stubGitHubForReview(t *testing.T, prNumber int) (*http.ServeMux, chan struct{}) {
	t.Helper()
	posted := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/pulls/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/files"):
			json.NewEncoder(w).Encode([]map[string]string{})
		case strings.HasSuffix(r.URL.Path, "/reviews") && r.Method == http.MethodGet:
			json.NewEncoder(w).Encode([]map[string]any{})
		case strings.HasSuffix(r.URL.Path, "/reviews") && r.Method == http.MethodPost:
			json.NewEncoder(w).Encode(map[string]any{"id": 1, "html_url": "https://x/1"})
			select {
			case posted <- struct{}{}:
			default:
			}
		case r.Header.Get("Accept") == "application/vnd.github.v3.diff":
			w.Write([]byte("diff --git a/a.go b/a.go\n"))
		default:
			json.NewEncoder(w).Encode(map[string]any{"mergeable": true, "mergeable_state": "clean"})
		}
	})
	mux.HandleFunc("/repos/acme/widgets/issues/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	return mux, posted
}

func waitForSignal(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the async review pipeline to run")
	}
}

func TestServeHTTP_ValidRequestTriggersReview(t *testing.T) {
	mux, posted := stubGitHubForReview(t, 7)
	fp := &fakeProvider{}
	h, _ := testHandler(t, mux, fp)

	body := pullRequestOpenedPayload(7)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", SignPayload(testSecret, body))
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("X-GitHub-Event", "pull_request")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	waitForSignal(t, posted)
}

func TestServeHTTP_InvalidMethodRejected(t *testing.T) {
	h, _ := testHandler(t, http.NewServeMux(), &fakeProvider{})

	req := httptest.NewRequest(http.MethodGet, "/webhooks/github", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestServeHTTP_OversizedBodyRejected(t *testing.T) {
	h, _ := testHandler(t, http.NewServeMux(), &fakeProvider{})

	oversized := strings.Repeat("a", maxBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(oversized))
	req.ContentLength = int64(len(oversized))
	req.Header.Set("X-Hub-Signature-256", SignPayload(testSecret, []byte(oversized)))
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("X-GitHub-Event", "pull_request")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestServeHTTP_InvalidSignatureRejected(t *testing.T) {
	h, _ := testHandler(t, http.NewServeMux(), &fakeProvider{})

	body := pullRequestOpenedPayload(7)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", "sha256=0000000000000000000000000000000000000000000000000000000000000000")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("X-GitHub-Event", "pull_request")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestServeHTTP_MissingDeliveryIDRejected(t *testing.T) {
	h, _ := testHandler(t, http.NewServeMux(), &fakeProvider{})

	body := pullRequestOpenedPayload(7)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", SignPayload(testSecret, body))
	req.Header.Set("X-GitHub-Event", "pull_request")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestServeHTTP_UnsupportedEventAcknowledgedWithoutReview(t *testing.T) {
	fp := &fakeProvider{}
	h, _ := testHandler(t, http.NewServeMux(), fp)

	body := []byte(`{"action":"labeled"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", SignPayload(testSecret, body))
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("X-GitHub-Event", "issues")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	time.Sleep(20 * time.Millisecond) // give any wrongly-spawned goroutine a chance to run
	if atomic.LoadInt32(&fp.calls) != 0 {
		t.Error("an unsupported event type must never invoke the review pipeline")
	}
}

func TestServeHTTP_UnsupportedPullRequestActionAcknowledgedWithoutReview(t *testing.T) {
	mux, posted := stubGitHubForReview(t, 7)
	fp := &fakeProvider{}
	h, _ := testHandler(t, mux, fp)

	body, _ := json.Marshal(map[string]any{
		"action":       "labeled",
		"pull_request": map[string]any{"number": 7, "head": map[string]string{"sha": "deadbeef"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", SignPayload(testSecret, body))
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("X-GitHub-Event", "pull_request")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	select {
	case <-posted:
		t.Fatal("a pull_request 'labeled' action must not trigger a review")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestServeHTTP_DuplicateDeliveryProcessedOnce(t *testing.T) {
	mux, posted := stubGitHubForReview(t, 7)
	fp := &fakeProvider{}
	h, _ := testHandler(t, mux, fp)

	body := pullRequestOpenedPayload(7)
	sig := SignPayload(testSecret, body)

	send := func() int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
		req.Header.Set("X-Hub-Signature-256", sig)
		req.Header.Set("X-GitHub-Delivery", "delivery-dup")
		req.Header.Set("X-GitHub-Event", "pull_request")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := send(); code != http.StatusAccepted {
		t.Fatalf("first delivery status = %d, want %d", code, http.StatusAccepted)
	}
	waitForSignal(t, posted)

	if code := send(); code != http.StatusOK {
		t.Fatalf("duplicate delivery status = %d, want %d", code, http.StatusOK)
	}
	if got := atomic.LoadInt32(&fp.calls); got != 1 {
		t.Errorf("provider.Review called %d times, want exactly 1 across both deliveries", got)
	}
}
