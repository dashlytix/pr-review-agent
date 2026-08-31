package orchestrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
	"github.com/dimension/ai-ci-agent/internal/notify"
	"github.com/dimension/ai-ci-agent/internal/provider"
)

// fakeProvider is a minimal provider.Provider for tests -- only Review is
// exercised by ReviewPR, so Assess/Answer just fail loudly if ever
// called by mistake, matching the pattern in internal/slackbot's own
// fakeProvider.
type fakeProvider struct {
	reviewFn func(ctx context.Context, req provider.AssessmentRequest) (provider.ReviewResult, error)
}

func (f *fakeProvider) Assess(ctx context.Context, req provider.AssessmentRequest) ([]provider.Assessment, error) {
	panic("Assess should not be called by ReviewPR")
}
func (f *fakeProvider) Review(ctx context.Context, req provider.AssessmentRequest) (provider.ReviewResult, error) {
	return f.reviewFn(ctx, req)
}
func (f *fakeProvider) Answer(ctx context.Context, req provider.AssessmentRequest, question string) (string, error) {
	panic("Answer should not be called by ReviewPR")
}

func testClient(t *testing.T, handler http.Handler) *ghclient.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := ghclient.New("test-token", "acme", "widgets")
	c.BaseURL = server.URL
	c.RetryBaseDelay = 5 * time.Millisecond
	return c
}

func TestReviewPR_PostsReviewOnSuccess(t *testing.T) {
	var postedBody string
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/vnd.github.v3.diff" {
			w.Write([]byte("diff --git a/a.go b/a.go\n"))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"mergeable": true, "mergeable_state": "clean"})
	})
	mux.HandleFunc("/repos/acme/widgets/pulls/7/files", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]string{})
	})
	mux.HandleFunc("/repos/acme/widgets/pulls/7/reviews", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]map[string]any{})
		case http.MethodPost:
			b := make([]byte, r.ContentLength)
			r.Body.Read(b)
			postedBody = string(b)
			json.NewEncoder(w).Encode(map[string]any{"id": 1, "html_url": "https://x/review/1"})
		}
	})

	client := testClient(t, mux)
	fp := &fakeProvider{reviewFn: func(ctx context.Context, req provider.AssessmentRequest) (provider.ReviewResult, error) {
		return provider.ReviewResult{Summary: "adds a feature"}, nil
	}}

	if err := ReviewPR(context.Background(), client, fp, 7, "deadbeef", notify.SlackConfig{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if postedBody == "" {
		t.Fatal("expected a review to be posted")
	}
}

func TestReviewPR_AlreadyPostedIsNotAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/vnd.github.v3.diff" {
			w.Write([]byte("diff\n"))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{})
	})
	mux.HandleFunc("/repos/acme/widgets/pulls/7/files", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]string{})
	})
	mux.HandleFunc("/repos/acme/widgets/pulls/7/reviews", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "body": "<!-- ai-ci-agent:review-marker:sha=deadbeef -->", "html_url": "https://x/1"},
			})
			return
		}
		t.Error("should not attempt to create a second review")
	})

	client := testClient(t, mux)
	// gather.GatherForReview and the LLM call both still run before the
	// marker lookup (ReviewPR has no way to short-circuit idempotency any
	// earlier than the post step) -- ReviewPR's actual idempotency
	// contract under test here is that a *second* review is never
	// created once the marker is already found, asserted by the reviews
	// mux handler above failing the test on any POST.
	fp := &fakeProvider{reviewFn: func(ctx context.Context, req provider.AssessmentRequest) (provider.ReviewResult, error) {
		return provider.ReviewResult{Summary: "x"}, nil
	}}

	if err := ReviewPR(context.Background(), client, fp, 7, "deadbeef", notify.SlackConfig{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReviewPR_GatherFailureDegradesToFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/repos/acme/widgets/pulls/7/reviews", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode([]map[string]any{})
			return
		}
		var in struct{ Body string }
		json.NewDecoder(r.Body).Decode(&in)
		if in.Body == "" {
			t.Error("expected a fallback body to be posted")
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 1, "html_url": "https://x/1"})
	})

	client := testClient(t, mux)
	client.MaxAttempts = 1 // don't burn real test time retrying the 500
	fp := &fakeProvider{reviewFn: func(ctx context.Context, req provider.AssessmentRequest) (provider.ReviewResult, error) {
		t.Error("provider should not be called when gather fails")
		return provider.ReviewResult{}, nil
	}}

	if err := ReviewPR(context.Background(), client, fp, 7, "deadbeef", notify.SlackConfig{}); err != nil {
		t.Fatalf("unexpected error (degrade path should still succeed in posting): %v", err)
	}
}

// notify.chatPostMessageURL is unexported with no test-only override
// hook, so HandlePullRequestEvent's Slack-enabled path is exercised by
// internal/notify's own tests, not from here. This test instead confirms
// the disabled-Slack path -- notify.Post is a documented no-op returning
// ("", nil) when SlackConfig isn't enabled -- correctly skips saving a
// thread root (there's no ts to save) rather than erroring or calling
// GitHub at all.
func TestHandlePullRequestEvent_OpenedWithSlackDisabledSkipsThreadSave(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/issues/9/comments", func(w http.ResponseWriter, r *http.Request) {
		t.Error("must not save a thread root when Slack is disabled -- there's no ts to save")
	})
	client := testClient(t, mux)

	event := &PullRequestEvent{Action: "opened"}
	event.PullRequest.Number = 9

	if err := HandlePullRequestEvent(context.Background(), client, event, "acme/widgets", notify.SlackConfig{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandlePullRequestEvent_UnrecognizedActionIsANoOp(t *testing.T) {
	client := testClient(t, http.NewServeMux())
	event := &PullRequestEvent{Action: "labeled"}
	if err := HandlePullRequestEvent(context.Background(), client, event, "acme/widgets", notify.SlackConfig{}); err != nil {
		t.Fatalf("unexpected error for an unrecognized action: %v", err)
	}
}

func TestShouldReview(t *testing.T) {
	tests := []struct {
		action string
		want   bool
	}{
		{"opened", true},
		{"reopened", true},
		{"synchronize", true},
		{"closed", false},
		{"labeled", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := ShouldReview(tt.action); got != tt.want {
			t.Errorf("ShouldReview(%q) = %v, want %v", tt.action, got, tt.want)
		}
	}
}
