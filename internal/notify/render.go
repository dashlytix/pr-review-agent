package notify

import (
	"fmt"
	"regexp"
	"strings"
)

// PullRequest carries the fields a Slack lifecycle notification needs.
// BaseRef/HeadSHA are only used by RenderMerged's Commit field; Title
// and Body are only used by RenderOpened (the root message's header and
// Summary field, respectively) -- thread replies omit them since
// they're already visible on the root message above.
type PullRequest struct {
	Number  int
	Title   string
	HTMLURL string
	Author  string
	Repo    string
	BaseRef string
	HeadSHA string
	Body    string
}

// SlackAttachmentMessage is a Slack incoming-webhook payload using a
// legacy attachment (not a bare top-level block list) specifically so the
// colored left border renders -- Slack only honors "color" inside
// attachments, not on top-level blocks.
type SlackAttachmentMessage struct {
	Attachments []SlackAttachment `json:"attachments"`
}

type SlackAttachment struct {
	Color    string       `json:"color"`
	Fallback string       `json:"fallback,omitempty"`
	Blocks   []SlackBlock `json:"blocks"`
}

type SlackBlock struct {
	Type     string      `json:"type"`
	Text     *SlackText  `json:"text,omitempty"`
	Fields   []SlackText `json:"fields,omitempty"`
	Elements []any       `json:"elements,omitempty"`
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

// Left-border colors for the five notifications, matching GitHub's own
// status coloring (green merge, red close/failure) with a neutral blue
// for "opened" and a distinct purple for the AI review reply.
const (
	colorOpened   = "#439FE0"
	colorMerged   = "#2EB67D"
	colorClosed   = "#E01E5A"
	colorCIFailed = "#ECB22E"
	colorReview   = "#611F69"
)

// RenderOpened builds the Slack notification for a newly opened PR --
// the one top-level/root message for that PR, which every other event
// (CI failure, AI review, closed) replies underneath as a thread reply.
// It adds a Summary block excerpted from the PR's own description
// (pull_request.body) when one is present. That excerpt is the author's
// own text from the webhook payload -- never the AI review agent's
// output, and never an LLM call. Summary gets its own full-width block
// rather than a fields-grid entry: Slack renders "fields" as a 2-column
// grid, and a short Status value next to a long multi-line excerpt
// squeezed both into half-width columns.
func RenderOpened(pr PullRequest) SlackAttachmentMessage {
	header := fmt.Sprintf("*PR #%d — %s*\n%s · opened by %s", pr.Number, pr.Title, pr.Repo, pr.Author)
	var extraBlocks []SlackBlock
	if excerpt := summaryExcerpt(pr.Body); excerpt != "" {
		extraBlocks = append(extraBlocks, SlackBlock{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*Summary:*\n%s", excerpt)}})
	}
	return threadCard(colorOpened, fmt.Sprintf("PR opened — PR #%d", pr.Number), header, "Opened", pr, nil, extraBlocks...)
}

// RenderClosed builds the Slack thread reply for a PR closed without
// being merged -- deliberately short, since it's just marking the
// thread's lifecycle as finished, not carrying new information.
func RenderClosed(pr PullRequest) SlackAttachmentMessage {
	return threadCard(colorClosed, "PR closed", "*PR closed*", "Closed", pr, nil)
}

// RenderMerged is RenderClosed's counterpart for a merged PR, adding a
// Commit field (short SHA + base branch) alongside the standard Status
// field.
func RenderMerged(pr PullRequest) SlackAttachmentMessage {
	commit := SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*Commit:*\n`%s` on `%s`", shortSHA(pr.HeadSHA), pr.BaseRef)}
	return threadCard(colorMerged, "PR merged", "*PR closed*", "Merged", pr, []SlackText{commit})
}

// RenderCIFailurePosted builds the Slack thread reply for a CI check
// failure. No diagnosis or findings text -- that's in the GitHub PR
// comment the AI CI agent posts separately. impactEmoji/impactLabel are
// the same 🔴 Critical / 🟡 Warning / 🟢 Good verdict the GitHub PR-review
// comment computes via post.OverallImpact, so the two surfaces never
// disagree about how bad a given run was -- pass "", "" to omit the
// field entirely (e.g. the diagnosis itself couldn't be produced).
func RenderCIFailurePosted(pr PullRequest, impactEmoji, impactLabel string) SlackAttachmentMessage {
	var extra []SlackText
	if impactEmoji != "" {
		extra = append(extra, SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*Impact:*\n%s %s", impactEmoji, impactLabel)})
	}
	return threadCard(colorCIFailed, "CI check failed", "*CI Check Failed*", "", pr, extra)
}

// RenderAIReview builds the Slack thread reply for a completed AI PR
// review -- summary, findings (loc + comment), and recommendations
// (suggested fixes), each section omitted when empty so a clean review
// doesn't show empty "Findings"/"Recommendations" headers. Deliberately
// takes plain strings rather than provider.Assessment/ReviewResult so
// this package stays independent of the review/assessment domain types
// -- callers translate.
func RenderAIReview(pr PullRequest, summary string, findings, recommendations []string) SlackAttachmentMessage {
	blocks := []SlackBlock{{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: "*AI Review*"}}}
	if summary != "" {
		blocks = append(blocks, SlackBlock{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*Summary*\n%s", summary)}})
	}
	if len(findings) > 0 {
		blocks = append(blocks, SlackBlock{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*Findings*\n%s", bulletList(findings))}})
	}
	if len(recommendations) > 0 {
		blocks = append(blocks, SlackBlock{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*Recommendations*\n%s", bulletList(recommendations))}})
	}
	return SlackAttachmentMessage{
		Attachments: []SlackAttachment{{
			Color:    colorReview,
			Fallback: fmt.Sprintf("AI Review — PR #%d", pr.Number),
			Blocks:   blocks,
		}},
	}
}

func bulletList(items []string) string {
	lines := make([]string, len(items))
	for i, item := range items {
		lines[i] = "• " + item
	}
	return strings.Join(lines, "\n")
}

// threadCard builds the single-attachment payload shared by the four PR
// lifecycle notifications (opened/closed/merged/CI-failed): a colored
// left border, a header section, an optional Status (+ extra) fields
// section, any extraBlocks needing the card's full width (e.g. Summary),
// and a "View PR" button. status == "" omits the fields section entirely
// (RenderCIFailurePosted, whose only field is the optional Impact one).
// Deliberately carries no review content, findings, or diagnosis text --
// that stays in the GitHub PR comment posted by internal/post's
// renderers, and (for AI review) in RenderAIReview's own thread reply.
func threadCard(color, fallback, header, status string, pr PullRequest, extraFields []SlackText, extraBlocks ...SlackBlock) SlackAttachmentMessage {
	blocks := []SlackBlock{{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: header}}}

	var fields []SlackText
	if status != "" {
		fields = append(fields, SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*Status:*\n%s", status)})
	}
	fields = append(fields, extraFields...)
	if len(fields) > 0 {
		blocks = append(blocks, SlackBlock{Type: "section", Fields: fields})
	}

	blocks = append(blocks, extraBlocks...)
	blocks = append(blocks, SlackBlock{Type: "actions", Elements: []any{SlackButton{
		Type:     "button",
		Text:     SlackText{Type: "plain_text", Text: "View PR"},
		URL:      pr.HTMLURL,
		ActionID: "view_pr",
	}}})
	return SlackAttachmentMessage{
		Attachments: []SlackAttachment{{
			Color:    color,
			Fallback: fmt.Sprintf("%s — PR #%d", fallback, pr.Number),
			Blocks:   blocks,
		}},
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// maxSummaryExcerptRunes caps the "Summary" field's rendered length to
// roughly match Slack's own compact-card conventions.
const maxSummaryExcerptRunes = 200

// maxSummaryExcerptLines caps how many lines/bullets the excerpt pulls,
// whether from a "Summary" section or the body's own start.
const maxSummaryExcerptLines = 3

var summaryHeadingPattern = regexp.MustCompile(`(?i)^#{0,6}\s*summary\s*:?\s*$`)

// summaryExcerpt pulls a short excerpt from a PR's own description
// (pull_request.body straight off the webhook payload -- never the AI
// review agent's output, and never an LLM call) for the "PR opened"
// Slack card's Summary field. It prefers the first few lines under a
// "Summary"/"## Summary" heading when the body has one, falling back to
// the body's own first few lines otherwise. Returns "" for an empty (or
// heading-only, content-free) body, so RenderOpened omits the field
// entirely instead of showing "Summary: (none)".
func summaryExcerpt(body string) string {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")

	if start := indexAfterSummaryHeading(lines); start >= 0 {
		if excerpt := collectSectionLines(lines[start:], maxSummaryExcerptLines); excerpt != "" {
			return truncateExcerpt(excerpt)
		}
	}
	return truncateExcerpt(collectBodyLines(lines, maxSummaryExcerptLines))
}

// indexAfterSummaryHeading returns the index of the first line after a
// "Summary" heading (bare or "## Summary"-style), or -1 if none exists.
func indexAfterSummaryHeading(lines []string) int {
	for i, line := range lines {
		if summaryHeadingPattern.MatchString(strings.TrimSpace(line)) {
			return i + 1
		}
	}
	return -1
}

// collectSectionLines collects up to n non-empty lines from the start of
// a section, stopping at the next markdown heading so the excerpt
// doesn't bleed into the next section (e.g. "## Test plan").
func collectSectionLines(lines []string, n int) string {
	var collected []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			break
		}
		if trimmed == "" {
			continue
		}
		collected = append(collected, trimmed)
		if len(collected) == n {
			break
		}
	}
	return strings.Join(collected, "\n")
}

// collectBodyLines collects up to n non-empty, non-heading lines from
// anywhere in the body, for the no-preferred-section fallback (no
// "Summary" heading at all, or one whose own section came up empty).
// Heading lines are skipped rather than treated as a stop boundary here,
// since there's no specific section being excerpted.
func collectBodyLines(lines []string, n int) string {
	var collected []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		collected = append(collected, trimmed)
		if len(collected) == n {
			break
		}
	}
	return strings.Join(collected, "\n")
}

func truncateExcerpt(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) > maxSummaryExcerptRunes {
		return string(r[:maxSummaryExcerptRunes]) + "…"
	}
	return s
}
