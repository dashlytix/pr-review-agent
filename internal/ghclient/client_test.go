package ghclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	c := New("test-token", "owner", "repo")
	c.BaseURL = server.URL
	c.RetryBaseDelay = 5 * time.Millisecond // real backoff would make tests slow for no benefit
	return c
}

func TestGetJSON_SendsAuthAndDecodesBody(t *testing.T) {
	var gotAuth, gotVersion string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotVersion = r.Header.Get("X-GitHub-Api-Version")
		json.NewEncoder(w).Encode(map[string]string{"hello": "world"})
	}))
	defer server.Close()

	c := testClient(t, server)
	var out map[string]string
	if err := c.GetJSON(context.Background(), "/whatever", &out); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization header = %q, want Bearer test-token", gotAuth)
	}
	if gotVersion != "2022-11-28" {
		t.Errorf("X-GitHub-Api-Version header = %q, want 2022-11-28", gotVersion)
	}
	if out["hello"] != "world" {
		t.Errorf("decoded body = %v, want {hello: world}", out)
	}
}

func TestRepoPath_BuildsExpectedPath(t *testing.T) {
	c := New("t", "acme", "widgets")
	got := c.RepoPath("/pulls/%d/files?per_page=%d", 7, 100)
	want := "/repos/acme/widgets/pulls/7/files?per_page=100"
	if got != want {
		t.Errorf("RepoPath = %q, want %q", got, want)
	}
}

func TestRetry_RecoversFromTransientRateLimit(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
	}))
	defer server.Close()

	c := testClient(t, server)
	var out map[string]string
	if err := c.GetJSON(context.Background(), "/anything", &out); err != nil {
		t.Fatalf("expected the second attempt to succeed, got error: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("expected exactly 2 requests (1 rate-limited + 1 success), got %d", calls)
	}
	if out["ok"] != "true" {
		t.Errorf("unexpected body after retry: %v", out)
	}
}

func TestRetry_ExhaustsAttemptsAndReturnsRateLimitedError(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	c := testClient(t, server)
	var out map[string]string
	err := c.GetJSON(context.Background(), "/anything", &out)

	var rl *RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("expected a *RateLimitedError after exhausting retries, got: %v", err)
	}
	if int(atomic.LoadInt32(&calls)) != c.MaxAttempts {
		t.Errorf("expected exactly MaxAttempts (%d) requests, got %d", c.MaxAttempts, calls)
	}
}

func TestGetJSON_NonRateLimitErrorIsNotRetried(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	c := testClient(t, server)
	var out map[string]string
	err := c.GetJSON(context.Background(), "/missing", &out)

	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
	var rl *RateLimitedError
	if errors.As(err, &rl) {
		t.Fatal("a plain 404 should not be classified as rate limiting")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("a non-rate-limit error should not be retried, got %d requests", calls)
	}
}

func TestPostJSON_SendsBodyAndDecodesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["body"] != "hello" {
			t.Errorf("server received body = %v, want {body: hello}", in)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "123"})
	}))
	defer server.Close()

	c := testClient(t, server)
	var out map[string]string
	err := c.PostJSON(context.Background(), "/issues/1/comments", map[string]string{"body": "hello"}, &out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["id"] != "123" {
		t.Errorf("decoded response = %v, want {id: 123}", out)
	}
}
