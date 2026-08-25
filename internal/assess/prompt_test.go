package assess

import (
	"strings"
	"testing"
)

func TestBuildAnswerPrompt_IncludesDiffFilesAndQuestion(t *testing.T) {
	req := AssessmentRequest{
		Diff:  "diff --git a/a.go b/a.go\n+ removed nil check",
		Files: map[string]string{"a.go": "func f() {}"},
	}
	got := BuildAnswerPrompt(req, "why did this fail?")

	for _, want := range []string{"## PR diff", "removed nil check", "## Touched files", "### a.go", "## Question\nwhy did this fail?"} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildAnswerPrompt() = %q, want it to contain %q", got, want)
		}
	}
}

func TestBuildAnswerPrompt_NoDiffOrFilesOmitsThemGracefully(t *testing.T) {
	got := BuildAnswerPrompt(AssessmentRequest{}, "what does this PR do?")

	for _, want := range []string{"(no diff available)", "(none)", "## Question\nwhat does this PR do?"} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildAnswerPrompt() = %q, want it to contain %q", got, want)
		}
	}
}

// BuildAnswerPrompt reuses BuildReviewPrompt's diff/files section rather
// than duplicating it -- this test pins that the two stay in sync (a
// question-less review prompt is a strict prefix of an answer prompt),
// so future changes to one don't silently drift from the other.
func TestBuildAnswerPrompt_DiffFilesSectionMatchesReviewPrompt(t *testing.T) {
	req := AssessmentRequest{Diff: "some diff", Files: map[string]string{"x.go": "content"}}

	reviewPrompt := BuildReviewPrompt(req)
	answerPrompt := BuildAnswerPrompt(req, "a question")

	if !strings.HasPrefix(answerPrompt, reviewPrompt) {
		t.Errorf("BuildAnswerPrompt() = %q, want it to start with BuildReviewPrompt()'s diff/files section %q", answerPrompt, reviewPrompt)
	}
}
