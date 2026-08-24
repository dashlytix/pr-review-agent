package notify

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderOpened_HasBlueBorderHeaderAndButton(t *testing.T) {
	pr := PullRequest{Number: 1, Repo: "widgets", HTMLURL: "https://github.com/o/r/pull/1", Author: "alice"}
	msg := RenderOpened(pr)

	if len(msg.Attachments) != 1 {
		t.Fatalf("len(Attachments) = %d, want 1", len(msg.Attachments))
	}
	att := msg.Attachments[0]
	if att.Color != colorOpened {
		t.Errorf("Color = %q, want %q", att.Color, colorOpened)
	}
	got, _ := json.Marshal(msg)
	for _, want := range []string{"PR opened — PR #1", "widgets", "alice", "Opened", "View PR", "https://github.com/o/r/pull/1"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("RenderOpened() = %s, want it to contain %q", got, want)
		}
	}
}

func TestRenderOpened_NoBodyOmitsSummaryField(t *testing.T) {
	pr := PullRequest{Number: 1, Repo: "widgets", HTMLURL: "https://github.com/o/r/pull/1", Author: "alice"}
	got, _ := json.Marshal(RenderOpened(pr))
	if strings.Contains(string(got), "Summary") {
		t.Errorf("RenderOpened() with empty body = %s, want no Summary field", got)
	}
}

func TestRenderOpened_WithBodyAddsSummaryField(t *testing.T) {
	body := "## Summary\n- Adds SQL injection protection to the quotes export query\n- Removes the hardcoded API credential from the export handler\n- Adds a regression test covering both\n\n## Test plan\n- [ ] Manually export quotes and verify escaping\n- [ ] Run the new regression test"
	pr := PullRequest{Number: 1, Repo: "widgets", HTMLURL: "https://github.com/o/r/pull/1", Author: "alice", Body: body}
	got, _ := json.Marshal(RenderOpened(pr))

	for _, want := range []string{"Summary", "SQL injection protection", "hardcoded API credential", "regression test covering both"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("RenderOpened() = %s, want it to contain %q", got, want)
		}
	}
	if strings.Contains(string(got), "Test plan") || strings.Contains(string(got), "Manually export") {
		t.Errorf("RenderOpened() = %s, want the excerpt to stop before the next heading", got)
	}
}

func TestRenderClosedAndMerged_NeverGetSummaryField(t *testing.T) {
	body := "## Summary\n- some change"
	pr := PullRequest{Number: 1, Repo: "widgets", HTMLURL: "https://github.com/o/r/pull/1", Author: "alice", Body: body}

	for name, msg := range map[string]SlackAttachmentMessage{
		"closed": RenderClosed(pr),
		"merged": RenderMerged(pr),
	} {
		got, _ := json.Marshal(msg)
		if strings.Contains(string(got), "Summary") {
			t.Errorf("%s: = %s, want no Summary field -- only RenderOpened should ever show one", name, got)
		}
	}
}

func TestSummaryExcerpt(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"empty body", "", ""},
		{
			"prefers Summary heading section",
			"## Summary\nfirst\nsecond\n\n## Test plan\nthird",
			"first\nsecond",
		},
		{
			"bare Summary heading without markdown hashes",
			"Summary\nfirst\nsecond",
			"first\nsecond",
		},
		{
			"falls back to body start when no heading",
			"first line\nsecond line\n\nthird line\nfourth line",
			"first line\nsecond line\nthird line",
		},
		{
			"empty Summary section falls back to whole body",
			"## Summary\n\n## Test plan\nsomething",
			"something",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summaryExcerpt(tt.body); got != tt.want {
				t.Errorf("summaryExcerpt(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestSummaryExcerpt_TruncatesLongTextWithEllipsis(t *testing.T) {
	long := strings.Repeat("a", 250)
	got := summaryExcerpt(long)
	if !strings.HasSuffix(got, "…") {
		t.Errorf("summaryExcerpt() = %q, want it to end with an ellipsis", got)
	}
	if len([]rune(got)) != maxSummaryExcerptRunes+1 { // +1 for the ellipsis rune
		t.Errorf("len(summaryExcerpt()) = %d, want %d", len([]rune(got)), maxSummaryExcerptRunes+1)
	}
}

func TestRenderClosed_HasRedBorder(t *testing.T) {
	pr := PullRequest{Number: 2, Repo: "widgets", HTMLURL: "https://github.com/o/r/pull/2", Author: "bob"}
	msg := RenderClosed(pr)

	if msg.Attachments[0].Color != colorClosed {
		t.Errorf("Color = %q, want %q", msg.Attachments[0].Color, colorClosed)
	}
	got, _ := json.Marshal(msg)
	for _, want := range []string{"PR closed — PR #2", "Closed"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("RenderClosed() = %s, want it to contain %q", got, want)
		}
	}
}

func TestRenderMerged_HasGreenBorderAndCommitField(t *testing.T) {
	pr := PullRequest{Number: 3, Repo: "widgets", HTMLURL: "https://github.com/o/r/pull/3", Author: "carol", BaseRef: "main", HeadSHA: "abc1234567"}
	msg := RenderMerged(pr)

	if msg.Attachments[0].Color != colorMerged {
		t.Errorf("Color = %q, want %q", msg.Attachments[0].Color, colorMerged)
	}
	got, _ := json.Marshal(msg)
	for _, want := range []string{"PR merged — PR #3", "Merged", "Commit", "abc1234", "main"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("RenderMerged() = %s, want it to contain %q", got, want)
		}
	}
	if strings.Contains(string(got), "abc1234567") {
		t.Errorf("RenderMerged() = %s, want the commit SHA shortened to 7 chars", got)
	}
}

func TestRenderMerged_OmitsCommitFieldForOtherEvents(t *testing.T) {
	pr := PullRequest{Number: 4, Repo: "widgets", HTMLURL: "https://github.com/o/r/pull/4", Author: "dave"}
	got, _ := json.Marshal(RenderOpened(pr))
	if strings.Contains(string(got), "Commit") {
		t.Errorf("RenderOpened() = %s, want no Commit field", got)
	}
}

func TestRenderCIFailurePosted_HasOrangeBorderAndNoDiagnosisText(t *testing.T) {
	pr := PullRequest{Number: 5, Repo: "widgets", HTMLURL: "https://github.com/o/r/pull/5", Author: "erin"}
	msg := RenderCIFailurePosted(pr)

	if msg.Attachments[0].Color != colorCIFailed {
		t.Errorf("Color = %q, want %q", msg.Attachments[0].Color, colorCIFailed)
	}
	got, _ := json.Marshal(msg)
	for _, want := range []string{"CI check failed — PR #5", "CI failed", "View PR"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("RenderCIFailurePosted() = %s, want it to contain %q", got, want)
		}
	}
}

// None of the four lifecycle notifications should ever carry review
// content -- findings, severity badges, or diagnosis text stay in the
// GitHub PR comment, not Slack.
func TestLifecycleNotifications_NeverCarryReviewContent(t *testing.T) {
	pr := PullRequest{Number: 6, Repo: "widgets", HTMLURL: "https://github.com/o/r/pull/6", Author: "frank", BaseRef: "main", HeadSHA: "abc1234"}
	for name, msg := range map[string]SlackAttachmentMessage{
		"opened":     RenderOpened(pr),
		"closed":     RenderClosed(pr),
		"merged":     RenderMerged(pr),
		"ci-failure": RenderCIFailurePosted(pr),
	} {
		if len(msg.Attachments) != 1 || len(msg.Attachments[0].Blocks) != 3 {
			t.Errorf("%s: expected exactly 1 attachment with 3 blocks (header, fields, button), got %+v", name, msg)
		}
		got, _ := json.Marshal(msg)
		for _, unwanted := range []string{"red_circle", "orange_circle", "critical", "warning", "finding"} {
			if strings.Contains(strings.ToLower(string(got)), unwanted) {
				t.Errorf("%s: RenderX() = %s, want no review/severity content (%q)", name, got, unwanted)
			}
		}
	}
}
