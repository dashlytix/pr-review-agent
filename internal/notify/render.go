package notify

import "fmt"

// PullRequest carries the fields a Slack lifecycle notification needs.
// BaseRef/HeadSHA are only used by RenderMerged's Commit field.
type PullRequest struct {
	Number  int
	HTMLURL string
	Author  string
	Repo    string
	BaseRef string
	HeadSHA string
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

// RenderOpened builds the Slack notification for a newly opened PR.
func RenderOpened(pr PullRequest) SlackAttachmentMessage {
	return lifecycleAttachment(colorOpened, "PR opened", "Opened", pr)
}

// RenderClosed builds the Slack notification for a PR closed without
// being merged.
func RenderClosed(pr PullRequest) SlackAttachmentMessage {
	return lifecycleAttachment(colorClosed, "PR closed", "Closed", pr)
}

// RenderMerged builds the Slack notification for a merged PR, adding a
// Commit field (short SHA + base branch) alongside the standard Status
// field.
func RenderMerged(pr PullRequest) SlackAttachmentMessage {
	commit := SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*Commit:*\n`%s` on `%s`", shortSHA(pr.HeadSHA), pr.BaseRef)}
	return lifecycleAttachment(colorMerged, "PR merged", "Merged", pr, commit)
}

// RenderCIFailurePosted builds the Slack notification for a CI check
// failure. No diagnosis text -- that's in the GitHub PR comment the AI CI
// agent posts separately; Slack only ever gets the fact that CI failed.
func RenderCIFailurePosted(pr PullRequest) SlackAttachmentMessage {
	return lifecycleAttachment(colorCIFailed, "CI check failed", "CI failed", pr)
}

// lifecycleAttachment builds the single-attachment payload shared by all
// four lifecycle notifications: a colored left border, a header +
// subtitle section, a Status (+ optional extra) fields section, and a
// "View PR" button. Deliberately carries no review content, findings, or
// diagnosis text -- that stays in the GitHub PR comment posted by
// internal/post's renderers.
func lifecycleAttachment(color, eventLabel, status string, pr PullRequest, extraFields ...SlackText) SlackAttachmentMessage {
	fields := append([]SlackText{{Type: "mrkdwn", Text: fmt.Sprintf("*Status:*\n%s", status)}}, extraFields...)
	return SlackAttachmentMessage{
		Attachments: []SlackAttachment{{
			Color:    color,
			Fallback: fmt.Sprintf("%s — PR #%d", eventLabel, pr.Number),
			Blocks: []SlackBlock{
				{Type: "section", Text: &SlackText{Type: "mrkdwn", Text: fmt.Sprintf("*%s — PR #%d*\n%s · opened by %s", eventLabel, pr.Number, pr.Repo, pr.Author)}},
				{Type: "section", Fields: fields},
				{Type: "actions", Elements: []any{SlackButton{
					Type:     "button",
					Text:     SlackText{Type: "plain_text", Text: "View PR"},
					URL:      pr.HTMLURL,
					ActionID: "view_pr",
				}}},
			},
		}},
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
