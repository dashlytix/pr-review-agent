package notify

import (
	"strings"
	"testing"
)

func TestRenderOpened_IncludesTitleURLAndAuthor(t *testing.T) {
	got := RenderOpened("Fix the thing", "https://github.com/o/r/pull/1", "alice")
	for _, want := range []string{"Fix the thing", "https://github.com/o/r/pull/1", "alice"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderOpened() = %q, want it to contain %q", got, want)
		}
	}
}

func TestRenderClosed_IncludesTitleURLAndAuthor(t *testing.T) {
	got := RenderClosed("Fix the thing", "https://github.com/o/r/pull/1", "alice")
	for _, want := range []string{"Fix the thing", "https://github.com/o/r/pull/1", "alice", "without merging"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderClosed() = %q, want it to contain %q", got, want)
		}
	}
}

func TestRenderMerged_ProdBranchSaysMergedToProduction(t *testing.T) {
	got := RenderMerged("Fix the thing", "https://github.com/o/r/pull/1", "alice", "main", "main")
	if !strings.Contains(got, "production") {
		t.Errorf("RenderMerged(base=main, prod=main) = %q, want it to mention production", got)
	}
}

func TestRenderMerged_NonProdBranchSaysPlainMerged(t *testing.T) {
	got := RenderMerged("Fix the thing", "https://github.com/o/r/pull/1", "alice", "develop", "main")
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
