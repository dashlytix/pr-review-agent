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
