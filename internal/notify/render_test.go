package notify

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestRenderOpened_IncludesTitleURLAndAuthor(t *testing.T) {
	got := RenderOpened(PullRequest{
		Number:  1,
		Title:   "Fix the thing",
		HTMLURL: "https://github.com/o/r/pull/1",
		Author:  "alice",
	})
	for _, want := range []string{"Fix the thing", "https://github.com/o/r/pull/1", "alice", "#1"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderOpened() = %q, want it to contain %q", got, want)
		}
	}
}

func TestRenderOpened_IncludesSummaryAndReviewers(t *testing.T) {
	got := RenderOpened(PullRequest{
		Number:    1,
		Title:     "Fix the thing",
		HTMLURL:   "https://github.com/o/r/pull/1",
		Author:    "alice",
		Body:      "- does the fix\n- adds a test",
		Reviewers: []string{"bob", "carol"},
	})
	for _, want := range []string{"Summary", "does the fix", "adds a test", "Reviewers", "bob", "carol"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderOpened() = %q, want it to contain %q", got, want)
		}
	}
}

func TestRenderOpened_OmitsSummaryAndReviewersWhenAbsent(t *testing.T) {
	got := RenderOpened(PullRequest{
		Number:  1,
		Title:   "Fix the thing",
		HTMLURL: "https://github.com/o/r/pull/1",
		Author:  "alice",
	})
	for _, unwanted := range []string{"Summary", "Reviewers"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("RenderOpened() with no body/reviewers = %q, want it not to contain %q", got, unwanted)
		}
	}
}

func TestRenderClosed_IncludesTitleURLAndAuthor(t *testing.T) {
	got := RenderClosed(PullRequest{
		Number:  1,
		Title:   "Fix the thing",
		HTMLURL: "https://github.com/o/r/pull/1",
		Author:  "alice",
	})
	for _, want := range []string{"Fix the thing", "https://github.com/o/r/pull/1", "alice", "without merging"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderClosed() = %q, want it to contain %q", got, want)
		}
	}
}

func TestRenderMerged_ProdBranchSaysMergedToProduction(t *testing.T) {
	pr := PullRequest{Number: 1, Title: "Fix the thing", HTMLURL: "https://github.com/o/r/pull/1", Author: "alice"}
	got := RenderMerged(pr, "main", "main")
	if !strings.Contains(got, "production") {
		t.Errorf("RenderMerged(base=main, prod=main) = %q, want it to mention production", got)
	}
}

func TestRenderMerged_NonProdBranchSaysPlainMerged(t *testing.T) {
	pr := PullRequest{Number: 1, Title: "Fix the thing", HTMLURL: "https://github.com/o/r/pull/1", Author: "alice"}
	got := RenderMerged(pr, "develop", "main")
	if strings.Contains(got, "production") {
		t.Errorf("RenderMerged(base=develop, prod=main) = %q, want it not to mention production", got)
	}
	if !strings.Contains(got, "develop") {
		t.Errorf("RenderMerged(base=develop, prod=main) = %q, want it to name the base branch", got)
	}
}

func TestRenderReviewPosted_NoFindingsShowsCheckAndSkipsBulletsAndButton(t *testing.T) {
	pr := PullRequest{Number: 152, Repo: "choice-sme-pricing", HTMLURL: "https://github.com/o/r/pull/152", Author: "fury0324"}
	msg := RenderReviewPosted(pr, nil)

	if len(msg.Blocks) != 2 {
		t.Fatalf("len(Blocks) = %d, want 2 (header + no-issues context)", len(msg.Blocks))
	}
	if msg.Blocks[1].Type != "context" || msg.Blocks[1].Elements == nil {
		t.Fatalf("Blocks[1] = %+v, want a context block with elements", msg.Blocks[1])
	}
	got, _ := json.Marshal(msg)
	for _, want := range []string{"Review posted on PR #152", "choice-sme-pricing", "fury0324", "white_check_mark", "No issues found"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("RenderReviewPosted() = %s, want it to contain %q", got, want)
		}
	}
	for _, unwanted := range []string{"actions", "button"} {
		if strings.Contains(string(got), unwanted) {
			t.Errorf("RenderReviewPosted() with no findings = %s, want it not to contain %q", got, unwanted)
		}
	}
}

func TestRenderReviewPosted_WithFindingsHasFourBlocksAndSeverityCounts(t *testing.T) {
	pr := PullRequest{Number: 152, Repo: "choice-sme-pricing", HTMLURL: "https://github.com/o/r/pull/152", Author: "fury0324"}
	findings := []Finding{
		{Title: "Possible SQL injection in quotes export query", Critical: true},
		{Title: "Hardcoded credential in export handler", Critical: true},
		{Title: "Missing input validation on price field", Critical: false},
	}
	msg := RenderReviewPosted(pr, findings)

	if len(msg.Blocks) != 4 {
		t.Fatalf("len(Blocks) = %d, want 4", len(msg.Blocks))
	}
	got, _ := json.Marshal(msg)
	for _, want := range []string{
		"red_circle", "2 critical", "large_orange_circle", "1 warning",
		"Possible SQL injection in quotes export query",
		"Hardcoded credential in export handler",
		"View full review", "https://github.com/o/r/pull/152", "button",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("RenderReviewPosted() = %s, want it to contain %q", got, want)
		}
	}
}

func TestRenderReviewPosted_MoreThanFiveFindingsAddsOverflowLine(t *testing.T) {
	findings := make([]Finding, 7)
	for i := range findings {
		findings[i] = Finding{Title: fmt.Sprintf("finding %d", i)}
	}
	msg := RenderReviewPosted(PullRequest{Number: 1, HTMLURL: "https://github.com/o/r/pull/1"}, findings)

	got, _ := json.Marshal(msg)
	if !strings.Contains(string(got), "+2 more in the full review") {
		t.Errorf("RenderReviewPosted() = %s, want it to contain the overflow line", got)
	}
	if strings.Contains(string(got), "finding 5") || strings.Contains(string(got), "finding 6") {
		t.Errorf("RenderReviewPosted() = %s, want only the first 5 findings listed", got)
	}
}

func TestRenderReviewUnavailable_DoesNotReadAsNoIssuesFound(t *testing.T) {
	pr := PullRequest{Number: 152, Repo: "choice-sme-pricing", HTMLURL: "https://github.com/o/r/pull/152", Author: "fury0324"}
	msg := RenderReviewUnavailable(pr, "the LLM provider was unavailable or timed out")

	got, _ := json.Marshal(msg)
	for _, want := range []string{"Review unavailable for PR #152", "LLM provider was unavailable", "View full review"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("RenderReviewUnavailable() = %s, want it to contain %q", got, want)
		}
	}
	if strings.Contains(string(got), "No issues found") {
		t.Errorf("RenderReviewUnavailable() = %s, want it not to read as a clean review", got)
	}
}

func TestRenderCIFailurePosted_HasSummaryAndButtonNoSeverityBadges(t *testing.T) {
	pr := PullRequest{Number: 42, Repo: "choice-sme-pricing", HTMLURL: "https://github.com/o/r/pull/42", Author: "fury0324"}
	msg := RenderCIFailurePosted(pr, "nil pointer dereference in the export handler")

	if len(msg.Blocks) != 3 {
		t.Fatalf("len(Blocks) = %d, want 3", len(msg.Blocks))
	}
	got, _ := json.Marshal(msg)
	for _, want := range []string{"CI failure on PR #42", "nil pointer dereference", "View full review"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("RenderCIFailurePosted() = %s, want it to contain %q", got, want)
		}
	}
	for _, unwanted := range []string{"red_circle", "large_orange_circle"} {
		if strings.Contains(string(got), unwanted) {
			t.Errorf("RenderCIFailurePosted() = %s, want it not to contain severity badges %q", got, unwanted)
		}
	}
}
