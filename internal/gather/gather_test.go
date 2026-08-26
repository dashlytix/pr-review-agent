package gather

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

const testLog = "=== job: test ===\n--- FAIL: TestGetOwner (0.00s)\npanic: runtime error: invalid memory address or nil pointer dereference\nvehicles/garage.go:42:5: nil check missing\nFAIL\tvehicles\t0.014s\n"

const testDiff = `diff --git a/vehicles/garage.go b/vehicles/garage.go
index 1111111..2222222 100644
--- a/vehicles/garage.go
+++ b/vehicles/garage.go
@@ -38,7 +38,7 @@ func NewGarage(owner *Person) *Garage {
 }

 func (g *Garage) Owner() string {
-	if g.owner == nil {
+	if g == nil {
 		return ""
 	}
 	return g.owner.Name
`

// mockGitHub serves the handful of endpoints Gather calls, matching the
// shapes documented in §2.1/§4.3.
func mockGitHub(t *testing.T, prNumberForCommitLookup int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/acme/widgets/actions/runs/123", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 123, "head_sha": "abc1234", "html_url": "https://github.com/acme/widgets/actions/runs/123",
		})
	})
	mux.HandleFunc("/repos/acme/widgets/actions/runs/123/jobs", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"jobs": []map[string]any{
				{"id": 999, "name": "test", "conclusion": "failure"},
				{"id": 998, "name": "lint", "conclusion": "success"},
			},
		})
	})
	mux.HandleFunc("/repos/acme/widgets/actions/jobs/999/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(testLog))
	})
	mux.HandleFunc("/repos/acme/widgets/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		// GetJSON (fetchMergeState) and GetRaw with the diff Accept header
		// (fetchDiff) share this same endpoint, distinguished only by the
		// Accept header each sends.
		if r.Header.Get("Accept") == "application/vnd.github.v3.diff" {
			w.Write([]byte(testDiff))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"mergeable": true, "mergeable_state": "clean"})
	})
	mux.HandleFunc("/repos/acme/widgets/pulls/42/files", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			json.NewEncoder(w).Encode([]any{})
			return
		}
		json.NewEncoder(w).Encode([]map[string]string{
			{"filename": "vehicles/garage.go", "patch": "@@ -38,7 +38,7 @@\n-old\n+new\n"},
		})
	})
	mux.HandleFunc("/repos/acme/widgets/commits/abc1234/pulls", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]int{{"number": prNumberForCommitLookup}})
	})

	return httptest.NewServer(mux)
}

func testClient(server *httptest.Server) *ghclient.Client {
	c := ghclient.New("test-token", "acme", "widgets")
	c.BaseURL = server.URL
	c.RetryBaseDelay = 5 * time.Millisecond
	return c
}

func TestGather_HappyPath(t *testing.T) {
	server := mockGitHub(t, 0)
	defer server.Close()
	client := testClient(server)

	result, err := Gather(context.Background(), client, 123, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42", result.PRNumber)
	}
	if result.HeadSHA != "abc1234" {
		t.Errorf("HeadSHA = %q, want abc1234", result.HeadSHA)
	}
	if !strings.Contains(result.Request.LogTail, "panic: runtime error") {
		t.Errorf("LogTail missing expected content: %q", result.Request.LogTail)
	}
	if !strings.Contains(result.Request.FailedTests, "--- FAIL: TestGetOwner") {
		t.Errorf("FailedTests should be extracted from the log tail, got: %q", result.Request.FailedTests)
	}
	if !strings.Contains(result.Request.Diff, "func (g *Garage) Owner()") {
		t.Errorf("Diff missing expected content: %q", result.Request.Diff)
	}
	if _, ok := result.Request.Files["vehicles/garage.go"]; !ok {
		t.Errorf("expected vehicles/garage.go in Files, got: %v", result.Request.Files)
	}
}

func TestGather_ResolvesPRNumberFromCommitWhenNotProvided(t *testing.T) {
	server := mockGitHub(t, 42)
	defer server.Close()
	client := testClient(server)

	result, err := Gather(context.Background(), client, 123, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.PRNumber != 42 {
		t.Errorf("PRNumber = %d, want 42 (resolved via commit lookup)", result.PRNumber)
	}
}

func TestGather_NoAssociatedPRReturnsNilResultAndNilError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/actions/runs/123", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": 123, "head_sha": "abc1234", "html_url": "https://x"})
	})
	mux.HandleFunc("/repos/acme/widgets/commits/abc1234/pulls", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]any{}) // no PRs associated
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := testClient(server)

	result, err := Gather(context.Background(), client, 123, 0)
	if result != nil || err != nil {
		t.Fatalf("expected (nil, nil) when no PR is associated with the run, got (%v, %v)", result, err)
	}
}

func TestGatherForReview_FetchesMergeState(t *testing.T) {
	server := mockGitHub(t, 0)
	defer server.Close()
	client := testClient(server)

	req, merge, err := GatherForReview(context.Background(), client, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(req.Diff, "func (g *Garage) Owner()") {
		t.Errorf("Diff missing expected content: %q", req.Diff)
	}
	if merge.MergeableState != "clean" || merge.Mergeable == nil || !*merge.Mergeable {
		t.Errorf("MergeState = %+v, want mergeable=true state=clean per the mock response", merge)
	}
	if merge.Conflicting() {
		t.Error("a clean, mergeable state must not be reported as conflicting")
	}
}

func TestGatherForReview_MergeStateFetchFailureIsNonFatal(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/pulls/42", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "application/vnd.github.v3.diff" {
			w.Write([]byte(testDiff))
			return
		}
		w.WriteHeader(http.StatusInternalServerError) // simulate the merge-state fetch failing
	})
	mux.HandleFunc("/repos/acme/widgets/pulls/42/files", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]any{})
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := testClient(server)
	client.MaxAttempts = 1

	req, merge, err := GatherForReview(context.Background(), client, 42)
	if err != nil {
		t.Fatalf("a failed merge-state fetch must not fail the whole gather, got: %v", err)
	}
	if !strings.Contains(req.Diff, "func (g *Garage) Owner()") {
		t.Errorf("Diff should still be populated despite the merge-state fetch failing, got: %q", req.Diff)
	}
	if merge.Conflicting() {
		t.Error("an unknown (failed-fetch) merge state must not be reported as conflicting")
	}
}

func TestMergeState_Conflicting(t *testing.T) {
	mergeableTrue, mergeableFalse := true, false
	tests := []struct {
		name string
		m    MergeState
		want bool
	}{
		{"dirty state", MergeState{MergeableState: "dirty", Mergeable: &mergeableFalse}, true},
		{"mergeable false, state not yet dirty", MergeState{MergeableState: "blocked", Mergeable: &mergeableFalse}, true},
		{"mergeable true, clean", MergeState{MergeableState: "clean", Mergeable: &mergeableTrue}, false},
		{"unknown/unset (not yet computed)", MergeState{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.Conflicting(); got != tt.want {
				t.Errorf("Conflicting() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGather_PartialFailureStillReturnsUsableResult(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/acme/widgets/actions/runs/123", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": 123, "head_sha": "abc1234", "html_url": "https://run-url"})
	})
	mux.HandleFunc("/repos/acme/widgets/actions/runs/123/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // simulate a failure fetching logs
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	client := testClient(server)
	client.MaxAttempts = 1 // don't waste test time retrying a 500

	result, err := Gather(context.Background(), client, 123, 42)
	if err == nil {
		t.Fatal("expected an error when the jobs endpoint fails")
	}
	if result == nil {
		t.Fatal("expected a partial Result (PRNumber/HeadSHA/RunHTMLURL) even though gather failed, so a fallback comment can still be posted (§7)")
	}
	if result.PRNumber != 42 || result.HeadSHA != "abc1234" || result.RunHTMLURL != "https://run-url" {
		t.Errorf("partial result missing expected metadata: %+v", result)
	}
}
