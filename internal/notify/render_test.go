package notify

import (
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

func TestRenderAssessmentPosted_IncludesTitleAndCommentURL(t *testing.T) {
	got := RenderAssessmentPosted("Fix the thing", "https://github.com/o/r/pull/1", "https://github.com/o/r/pull/1#issuecomment-1")
	for _, want := range []string{"Fix the thing", "https://github.com/o/r/pull/1#issuecomment-1"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderAssessmentPosted() = %q, want it to contain %q", got, want)
		}
	}
}
