package post

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
)

func testClient(server *httptest.Server) *ghclient.Client {
	c := ghclient.New("test-token", "acme", "widgets")
	c.BaseURL = server.URL
	c.RetryBaseDelay = 5 * time.Millisecond
	return c
}

func TestPost_CreatesReviewWhenNoneExists(t *testing.T) {
	var listCalls, createCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listCalls++
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "body": "an unrelated human review", "html_url": "https://x/1"},
			})
		case http.MethodPost:
			createCalls++
			var in reviewRequest
			json.NewDecoder(r.Body).Decode(&in)
			if !strings.Contains(in.Body, marker("abc1234")) {
				t.Errorf("posted review body should embed the marker for its SHA, got: %s", in.Body)
			}
			if in.Event != "COMMENT" {
				t.Errorf("Event = %q, want COMMENT -- this agent never blocks or approves", in.Event)
			}
			if in.CommitID != "abc1234" {
				t.Errorf("CommitID = %q, want abc1234", in.CommitID)
			}
			if len(in.Comments) != 1 || in.Comments[0].Path != "a.go" || in.Comments[0].Line != 5 || in.Comments[0].Side != "RIGHT" {
				t.Errorf("Comments = %+v, want one inline comment at a.go:5 on the RIGHT side", in.Comments)
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": 2, "html_url": "https://x/2"})
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	comments := []ReviewComment{{Path: "a.go", Line: 5, Body: "finding body"}}
	url, alreadyPosted, err := Post(context.Background(), testClient(server), 1, "abc1234", "summary "+marker("abc1234"), comments)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alreadyPosted {
		t.Error("expected a fresh post, not alreadyPosted")
	}
	if url != "https://x/2" {
		t.Errorf("url = %q, want https://x/2", url)
	}
	if listCalls != 1 || createCalls != 1 {
		t.Errorf("expected exactly one list + one create call, got list=%d create=%d", listCalls, createCalls)
	}
}

func TestPost_ReturnsExistingWithoutDuplicating(t *testing.T) {
	var listCalls, createCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			listCalls++
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "body": "already here: " + marker("abc1234"), "html_url": "https://x/existing"},
			})
		case http.MethodPost:
			createCalls++
			t.Error("should not create a duplicate review when one already carries the marker")
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	url, alreadyPosted, err := Post(context.Background(), testClient(server), 1, "abc1234", "new summary "+marker("abc1234"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !alreadyPosted {
		t.Error("expected alreadyPosted=true when a marker review already exists")
	}
	if url != "https://x/existing" {
		t.Errorf("url = %q, want the existing review's URL", url)
	}
	if createCalls != 0 {
		t.Errorf("expected 0 create calls, got %d", createCalls)
	}
}

// A CI-failure review already posted for this SHA must not make
// PostReview think a plain-review review already exists too (and vice
// versa) — they're independent idempotency lookups sharing a PR.
func TestPostReview_DoesNotCollideWithCIFailureMarker(t *testing.T) {
	var createCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "body": "CI failure review " + marker("abc1234"), "html_url": "https://x/1"},
			})
		case http.MethodPost:
			createCalls++
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": 2, "html_url": "https://x/review"})
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	url, alreadyPosted, err := PostReview(context.Background(), testClient(server), 1, "abc1234", "review summary "+reviewMarker("abc1234"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alreadyPosted {
		t.Error("PostReview must not see the CI-failure marker as its own")
	}
	if url != "https://x/review" {
		t.Errorf("url = %q, want the newly created review's URL", url)
	}
	if createCalls != 1 {
		t.Errorf("expected exactly 1 create call, got %d", createCalls)
	}
}

func TestExists_TrueOnlyForMatchingSHA(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/pulls/1/reviews", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "body": "posted for a different commit " + marker("def5678")},
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := testClient(server)

	exists, err := Exists(context.Background(), client, 1, "def5678")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Error("expected Exists to find the def5678 marker")
	}

	exists, err = Exists(context.Background(), client, 1, "abc1234")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Error("Exists must not match a different SHA's marker")
	}
}
