package notify

import (
	"fmt"
	"regexp"
	"strings"
)

// PullRequest carries the fields a Slack lifecycle notification needs.
// BaseRef/HeadSHA are only used by RenderMerged's Commit field; Body is
// only used by RenderOpened's Summary field.
type PullRequest struct {
	Number  int
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

// Left-border colors for the four lifecycle events, matching GitHub's own
// status coloring (green merge, red close/failure) with a neutral blue
// for "opened".
const (
	colorOpened   = "#439FE0"
	colorMerged   = "#2EB67D"
	colorClosed   = "#E01E5A"
	colorCIFailed = "#ECB22E"
)

// RenderOpened builds the Slack notification for a newly opened PR,
// adding a Summary block excerpted from the PR's own description
// (pull_request.body) when one is present. That excerpt is the author's
// own text from the webhook payload -- never the AI review agent's
// output, and never an LLM call. Summary gets its own full-width block
// rather than a fields-grid entry: Slack renders "fields" as a 2-column
// grid, and a short Status value next to a long multi-line excerpt
// squeezed both into half-width columns.
func RenderOpened(pr PullRequest) SlackAttachmentMessage {
	var extraBlocks []SlackBlock
	if excerpt := summaryExcerpt(pr.Body); excerpt != "" {
		extraBlocks = append(extraBlocks, SlackBlock{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*Summary:*\n%s", excerpt)}})
	}
	return lifecycleAttachment(colorOpened, "PR opened", "Opened", pr, nil, extraBlocks...)
}

// RenderClosed builds the Slack notification for a PR closed without
// being merged.
func RenderClosed(pr PullRequest) SlackAttachmentMessage {
	return lifecycleAttachment(colorClosed, "PR closed", "Closed", pr, nil)
}

// RenderMerged builds the Slack notification for a merged PR, adding a
// Commit field (short SHA + base branch) alongside the standard Status
// field -- both short, single-line values, so the fields grid fits them
// fine.
func RenderMerged(pr PullRequest) SlackAttachmentMessage {
	commit := SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*Commit:*\n`%s` on `%s`", shortSHA(pr.HeadSHA), pr.BaseRef)}
	return lifecycleAttachment(colorMerged, "PR merged", "Merged", pr, []SlackText{commit})
}

// RenderCIFailurePosted builds the Slack notification for a CI check
// failure. No diagnosis text -- that's in the GitHub PR comment the AI CI
// agent posts separately; Slack only ever gets the fact that CI failed.
func RenderCIFailurePosted(pr PullRequest) SlackAttachmentMessage {
	return lifecycleAttachment(colorCIFailed, "CI check failed", "CI failed", pr, nil)
}

// lifecycleAttachment builds the single-attachment payload shared by all
// four lifecycle notifications: a colored left border, a header +
// subtitle section, a Status (+ optional extra) fields section, any
// extraBlocks needing the card's full width (e.g. Summary), and a
// "View PR" button. Deliberately carries no review content, findings, or
// diagnosis text -- that stays in the GitHub PR comment posted by
// internal/post's renderers.
func lifecycleAttachment(color, eventLabel, status string, pr PullRequest, extraFields []SlackText, extraBlocks ...SlackBlock) SlackAttachmentMessage {
	fields := append([]SlackText{{Type: "mrkdwn", Text: fmt.Sprintf("*Status:*\n%s", status)}}, extraFields...)
	blocks := []SlackBlock{
		{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*%s — PR #%d*\n%s · opened by %s", eventLabel, pr.Number, pr.Repo, pr.Author)}},
		{Type: "section", Fields: fields},
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
			Fallback: fmt.Sprintf("%s — PR #%d", eventLabel, pr.Number),
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
