package notify

import (
	"fmt"
	"sort"
	"strings"
)

// PullRequest carries the fields a PR lifecycle notification may need.
// Not every Render* function uses every field (e.g. RenderClosed ignores
// Body and Reviewers).
type PullRequest struct {
	Number    int
	Title     string
	HTMLURL   string
	Author    string
	Repo      string
	Body      string
	Reviewers []string
}

// Finding is the minimal shape RenderReviewPosted needs from an assessment:
// a short title for the bullet list, and whether it's critical. Assessment
// has no dedicated title field, so callers derive Title from its prose
// Comment; Critical buckets the five-value P0..nit severity scale down to
// the two badges the compact card shows (P0 -> critical, everything else
// -> warning), since P0 is documented as reserved for a security-relevant
// break.
type Finding struct {
	Title    string
	Critical bool
}

// SlackMessage is a Slack Block Kit payload for an incoming webhook. Text
// is the fallback shown in notifications/screen readers -- it isn't a
// block itself, so it doesn't count against a message's block budget.
type SlackMessage struct {
	Text   string       `json:"text,omitempty"`
	Blocks []SlackBlock `json:"blocks"`
}

type SlackBlock struct {
	Type     string     `json:"type"`
	Text     *SlackText `json:"text,omitempty"`
	Elements []any      `json:"elements,omitempty"`
}

type SlackText struct {
	Type string `json:"type"` // "mrkdwn" or "plain_text"
	Text string `json:"text"`
}

type SlackButton struct {
	Type     string    `json:"type"` // "button"
	Text     SlackText `json:"text"`
	URL      string    `json:"url"`
	ActionID string    `json:"action_id"`
}

// maxBodySummaryRunes caps how much of a PR description gets relayed to
// Slack, so a long PR body doesn't dwarf the rest of the message.
const maxBodySummaryRunes = 1200

// maxReviewFindings caps how many finding titles the review card lists
// before collapsing the rest into a "+N more" line.
const maxReviewFindings = 5

// maxFailureSummaryRunes keeps the CI-failure card's diagnosis to roughly
// 1-2 lines.
const maxFailureSummaryRunes = 220

// RenderOpened builds the Slack notification text for a newly opened PR,
// including its description as a "Summary" section and any requested
// reviewers, so the channel gets the same context a reader would get by
// opening the PR itself.
func RenderOpened(pr PullRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🆕 Pull request opened by *%s*\n<%s|#%d %s>", pr.Author, pr.HTMLURL, pr.Number, pr.Title)
	if summary := truncate(pr.Body, maxBodySummaryRunes); summary != "" {
		fmt.Fprintf(&b, "\n\n*Summary*\n%s", summary)
	}
	if len(pr.Reviewers) > 0 {
		fmt.Fprintf(&b, "\n\n*Reviewers:* %s", strings.Join(pr.Reviewers, ", "))
	}
	return b.String()
}

// RenderClosed builds the Slack notification text for a PR closed without
// being merged.
func RenderClosed(pr PullRequest) string {
	return fmt.Sprintf("🚫 Pull request closed without merging by *%s*\n<%s|#%d %s>", pr.Author, pr.HTMLURL, pr.Number, pr.Title)
}

// RenderMerged builds the Slack notification text for a merged PR,
// distinguishing a merge into prodBranch ("merged to production") from
// any other base branch.
func RenderMerged(pr PullRequest, baseRef, prodBranch string) string {
	if baseRef == prodBranch {
		return fmt.Sprintf("🚀 Pull request merged to production (%s) by *%s*\n<%s|#%d %s>", baseRef, pr.Author, pr.HTMLURL, pr.Number, pr.Title)
	}
	return fmt.Sprintf("✅ Pull request merged into %s by *%s*\n<%s|#%d %s>", baseRef, pr.Author, pr.HTMLURL, pr.Number, pr.Title)
}

// RenderReviewPosted builds the compact Slack Block Kit message for a
// finished PR review: a header/subtitle section, a one-line severity
// summary (or a "no issues found" line, with blocks 3-4 dropped
// entirely), up to maxReviewFindings short finding titles sorted critical-
// first, and a button linking to the full review. Capped at 4 blocks
// total so the card stays scannable instead of dumping the raw markdown
// review body. Only call this when the review actually ran to completion
// -- an empty findings slice here is read as "no issues found"; a review
// that never ran (LLM outage, rate limit, malformed output) must use
// RenderReviewUnavailable instead so a failed review never reads as a
// clean one.
func RenderReviewPosted(pr PullRequest, findings []Finding) SlackMessage {
	header := SlackBlock{
		Type: "section",
		Text: &SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*Review posted on PR #%d*\n%s · opened by %s", pr.Number, pr.Repo, pr.Author)},
	}

	if len(findings) == 0 {
		return SlackMessage{
			Text: fmt.Sprintf("Review posted on PR #%d: no issues found", pr.Number),
			Blocks: []SlackBlock{
				header,
				{Type: "context", Elements: []any{SlackText{Type: "mrkdwn", Text: ":white_check_mark: No issues found"}}},
			},
		}
	}

	sorted := make([]Finding, len(findings))
	copy(sorted, findings)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Critical && !sorted[j].Critical })

	var critical, warning int
	for _, f := range sorted {
		if f.Critical {
			critical++
		} else {
			warning++
		}
	}

	var summary strings.Builder
	if critical > 0 {
		fmt.Fprintf(&summary, ":red_circle: %d critical", critical)
	}
	if warning > 0 {
		if summary.Len() > 0 {
			summary.WriteString("   ")
		}
		fmt.Fprintf(&summary, ":large_orange_circle: %d warning", warning)
	}

	shown := sorted
	if len(shown) > maxReviewFindings {
		shown = shown[:maxReviewFindings]
	}
	var bullets strings.Builder
	for i, f := range shown {
		if i > 0 {
			bullets.WriteString("\n")
		}
		fmt.Fprintf(&bullets, "• %s", f.Title)
	}
	if extra := len(sorted) - len(shown); extra > 0 {
		fmt.Fprintf(&bullets, "\n+%d more in the full review", extra)
	}

	return SlackMessage{
		Text: fmt.Sprintf("Review posted on PR #%d: %d critical, %d warning", pr.Number, critical, warning),
		Blocks: []SlackBlock{
			header,
			{Type: "context", Elements: []any{SlackText{Type: "mrkdwn", Text: summary.String()}}},
			{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: bullets.String()}},
			{Type: "actions", Elements: []any{SlackButton{
				Type:     "button",
				Text:     SlackText{Type: "plain_text", Text: "View full review"},
				URL:      pr.HTMLURL,
				ActionID: "view_full_review",
			}}},
		},
	}
}

// RenderReviewUnavailable builds the compact Slack Block Kit message for
// the review path's degrade-gracefully cases (§7): the LLM was
// unavailable or timed out, GitHub rate limited the diff fetch, or the
// model's output stayed malformed after the one bounded repair attempt.
// Distinct from RenderReviewPosted's "no issues found" case so a review
// that never ran never reads as a clean one.
func RenderReviewUnavailable(pr PullRequest, reason string) SlackMessage {
	return SlackMessage{
		Text: fmt.Sprintf("Review unavailable for PR #%d", pr.Number),
		Blocks: []SlackBlock{
			{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*Review unavailable for PR #%d*\n%s · opened by %s", pr.Number, pr.Repo, pr.Author)}},
			{Type: "context", Elements: []any{SlackText{Type: "mrkdwn", Text: fmt.Sprintf(":warning: %s", reason)}}},
			{Type: "actions", Elements: []any{SlackButton{
				Type:     "button",
				Text:     SlackText{Type: "plain_text", Text: "View full review"},
				URL:      pr.HTMLURL,
				ActionID: "view_full_review",
			}}},
		},
	}
}

// RenderCIFailurePosted builds the compact Slack Block Kit message for a
// CI-failure diagnosis comment: header, a short 1-2 line failure summary
// (no severity badges -- a CI failure is definitionally the top-priority
// finding), and the same "View full review" button.
func RenderCIFailurePosted(pr PullRequest, summary string) SlackMessage {
	return SlackMessage{
		Text: fmt.Sprintf("CI failure on PR #%d", pr.Number),
		Blocks: []SlackBlock{
			{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*CI failure on PR #%d*\n%s · opened by %s", pr.Number, pr.Repo, pr.Author)}},
			{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: truncate(summary, maxFailureSummaryRunes)}},
			{Type: "actions", Elements: []any{SlackButton{
				Type:     "button",
				Text:     SlackText{Type: "plain_text", Text: "View full review"},
				URL:      pr.HTMLURL,
				ActionID: "view_full_review",
			}}},
		},
	}
}

// truncate trims s to a Slack-message-friendly length.
func truncate(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) > maxRunes {
		return string(r[:maxRunes]) + "…"
	}
	return s
}
