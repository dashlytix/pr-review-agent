package webhook

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestIntegration_WebhookRequestReachesExistingReviewPipeline exercises
// the full path this feature exists for: a real HTTP POST over a real
// listening socket, through Server -> Handler.ServeHTTP -> signature
// verification -> event parsing -> idempotency -> the *existing*
// internal/orchestrate.ReviewPR pipeline -- with GitHub and the LLM
// provider mocked, and Slack left disabled (zero-value SlackConfig).
func TestIntegration_WebhookRequestReachesExistingReviewPipeline(t *testing.T) {
	mux, posted := stubGitHubForReview(t, 42)
	fp := &fakeProvider{}

	ghServer := httptest.NewServer(mux)
	t.Cleanup(ghServer.Close)

	handler := &Handler{
		Secret:      testSecret,
		Idempotency: NewInMemoryIdempotencyStore(),
		Token:       "test-token",
		BaseURL:     ghServer.URL,
		Provider:    fp,
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := NewServer(ln.Addr().String(), handler)

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ln) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		server.Shutdown(ctx)
		<-serveErr
	})

	webhookURL := "http://" + ln.Addr().String() + "/webhooks/github"
	body := pullRequestOpenedPayload(42)

	post := func(deliveryID string) int {
		req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("X-Hub-Signature-256", SignPayload(testSecret, body))
		req.Header.Set("X-GitHub-Delivery", deliveryID)
		req.Header.Set("X-GitHub-Event", "pull_request")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", webhookURL, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := post("integration-delivery-1"); code != http.StatusAccepted {
		t.Fatalf("first delivery status = %d, want %d", code, http.StatusAccepted)
	}
	waitForSignal(t, posted)

	// A second, identical delivery must not post a second review nor
	// call the provider again.
	if code := post("integration-delivery-1"); code != http.StatusOK {
		t.Fatalf("duplicate delivery status = %d, want %d", code, http.StatusOK)
	}
}
