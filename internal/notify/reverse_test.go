package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestFindPRByThreadRoot_MatchesCorrectOpenPR(t *testing.T) {
	comments := map[int][]issueComment{
		5: {{ID: 1, Body: "<!-- ai-ci-agent:slack-thread:ts=1700000000.000100 -->"}},
		7: {{ID: 2, Body: "<!-- ai-ci-agent:slack-thread:ts=1700000000.000200 -->"}},
	}
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]pullRequestSummary{{Number: 5}, {Number: 7}})
		case r.URL.Path == "/repos/owner/repo/issues/5/comments":
			json.NewEncoder(w).Encode(comments[5])
		case r.URL.Path == "/repos/owner/repo/issues/7/comments":
			json.NewEncoder(w).Encode(comments[7])
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	})

	prNumber, found, err := FindPRByThreadRoot(context.Background(), client, "1700000000.000200")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found || prNumber != 7 {
		t.Errorf("prNumber=%d found=%v, want prNumber=7 found=true", prNumber, found)
	}
}

func TestFindPRByThreadRoot_NoMatchReturnsFoundFalse(t *testing.T) {
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]pullRequestSummary{{Number: 5}})
		case r.URL.Path == "/repos/owner/repo/issues/5/comments":
			json.NewEncoder(w).Encode([]issueComment{{ID: 1, Body: "unrelated comment"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	})

	_, found, err := FindPRByThreadRoot(context.Background(), client, "1700000000.000999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false when no open PR's thread root matches")
	}
}

func TestFindPRByThreadRoot_NoOpenPRsReturnsFoundFalse(t *testing.T) {
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]pullRequestSummary{})
	})

	_, found, err := FindPRByThreadRoot(context.Background(), client, "1700000000.000100")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("expected found=false with no open PRs to scan")
	}
}

func TestListOpenPRThreadRoots_BuildsFullMapSkippingPRsWithNoRoot(t *testing.T) {
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]pullRequestSummary{{Number: 1}, {Number: 2}, {Number: 3}})
		case r.URL.Path == "/repos/owner/repo/issues/1/comments":
			json.NewEncoder(w).Encode([]issueComment{{ID: 1, Body: "<!-- ai-ci-agent:slack-thread:ts=ts-for-pr-1 -->"}})
		case r.URL.Path == "/repos/owner/repo/issues/2/comments":
			json.NewEncoder(w).Encode([]issueComment{}) // no marker -- e.g. Slack was disabled when opened
		case r.URL.Path == "/repos/owner/repo/issues/3/comments":
			json.NewEncoder(w).Encode([]issueComment{{ID: 2, Body: "<!-- ai-ci-agent:slack-thread:ts=ts-for-pr-3 -->"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	})

	roots, err := ListOpenPRThreadRoots(context.Background(), client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := map[string]int{"ts-for-pr-1": 1, "ts-for-pr-3": 3}
	if len(roots) != len(want) || roots["ts-for-pr-1"] != 1 || roots["ts-for-pr-3"] != 3 {
		t.Errorf("roots = %+v, want %+v (PR 2 omitted, no marker comment)", roots, want)
	}
}

func TestFindPRByThreadRoot_ShortCircuitsOnFirstMatch(t *testing.T) {
	var commentLookups int
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]pullRequestSummary{{Number: 1}, {Number: 2}, {Number: 3}})
		default:
			commentLookups++
			json.NewEncoder(w).Encode([]issueComment{{ID: 1, Body: fmt.Sprintf("<!-- ai-ci-agent:slack-thread:ts=matched-at-%s -->", r.URL.Path)}})
		}
	})

	// PR 1's marker won't match; PR 2's will, so PR 3 should never be looked up.
	_, found, err := FindPRByThreadRoot(context.Background(), client, "matched-at-/repos/owner/repo/issues/2/comments")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected a match on PR 2")
	}
	if commentLookups != 2 {
		t.Errorf("expected exactly 2 comment lookups (short-circuiting before PR 3), got %d", commentLookups)
	}
}
