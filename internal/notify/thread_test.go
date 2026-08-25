package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
)

func testGHClient(t *testing.T, handler http.HandlerFunc) *ghclient.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := ghclient.New("test-token", "owner", "repo")
	c.BaseURL = server.URL
	return c
}

func TestSaveThreadRoot_PostsMarkerIssueComment(t *testing.T) {
	var gotPath string
	var gotBody map[string]string
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusCreated)
	})

	if err := SaveThreadRoot(context.Background(), client, 24, "1700000000.000100"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/repos/owner/repo/issues/24/comments" {
		t.Errorf("path = %q, want the PR's issue-comments endpoint", gotPath)
	}
	if extractThreadTS(gotBody["body"]) != "1700000000.000100" {
		t.Errorf("posted comment body = %q, want it to embed the ts marker", gotBody["body"])
	}
}

func TestFindThreadRoot_FindsMarkerAmongOtherComments(t *testing.T) {
	comments := []issueComment{
		{ID: 1, Body: "unrelated human comment"},
		{ID: 2, Body: "_internal_\n<!-- ai-ci-agent:slack-thread:ts=1700000000.000100 -->"},
	}
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(comments)
	})

	ts, err := FindThreadRoot(context.Background(), client, 24)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts != "1700000000.000100" {
		t.Errorf("ts = %q, want the marker's ts", ts)
	}
}

func TestFindThreadRoot_NoMarkerReturnsEmpty(t *testing.T) {
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]issueComment{{ID: 1, Body: "just a regular comment"}})
	})

	ts, err := FindThreadRoot(context.Background(), client, 24)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts != "" {
		t.Errorf("ts = %q, want empty when no marker comment exists", ts)
	}
}

func TestSaveThenFindThreadRoot_RoundTrips(t *testing.T) {
	var saved []issueComment
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			saved = append(saved, issueComment{ID: int64(len(saved) + 1), Body: body["body"]})
			w.WriteHeader(http.StatusCreated)
			return
		}
		json.NewEncoder(w).Encode(saved)
	})

	if err := SaveThreadRoot(context.Background(), client, 24, "1700000000.000300"); err != nil {
		t.Fatalf("unexpected error saving: %v", err)
	}
	ts, err := FindThreadRoot(context.Background(), client, 24)
	if err != nil {
		t.Fatalf("unexpected error finding: %v", err)
	}
	if ts != "1700000000.000300" {
		t.Errorf("ts = %q, want the ts just saved", ts)
	}
}
