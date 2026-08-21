package notify

import (
	"fmt"
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
	Body      string
	Reviewers []string
}

// maxBodySummaryRunes caps how much of a PR description gets relayed to
// Slack, so a long PR body doesn't dwarf the rest of the message.
const maxBodySummaryRunes = 1200

// RenderOpened builds the Slack notification text for a newly opened PR,
// including its description as a "Summary" section and any requested
// reviewers, so the channel gets the same context a reader would get by
// opening the PR itself.
func RenderOpened(pr PullRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🆕 Pull request opened by *%s*\n<%s|#%d %s>", pr.Author, pr.HTMLURL, pr.Number, pr.Title)
	if summary := truncateBody(pr.Body); summary != "" {
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

// RenderAssessmentPosted builds the Slack notification text for the AI
// review/CI-failure comment being posted on a PR.
func RenderAssessmentPosted(title, htmlURL, commentURL string) string {
	return fmt.Sprintf("🤖 AI review posted on <%s|%s>: %s", htmlURL, title, commentURL)
}

// truncateBody trims a PR description to a Slack-message-friendly length.
func truncateBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	r := []rune(body)
	if len(r) > maxBodySummaryRunes {
		return string(r[:maxBodySummaryRunes]) + "…"
	}
	return body
}
