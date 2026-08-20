package notify

import "fmt"

// RenderOpened builds the Slack notification text for a newly opened PR.
func RenderOpened(title, htmlURL, author string) string {
	return fmt.Sprintf("🆕 PR opened: <%s|%s> by %s", htmlURL, title, author)
}

// RenderClosed builds the Slack notification text for a PR closed without
// being merged.
func RenderClosed(title, htmlURL, author string) string {
	return fmt.Sprintf("🚫 PR closed without merging: <%s|%s> by %s", htmlURL, title, author)
}

// RenderMerged builds the Slack notification text for a merged PR,
// distinguishing a merge into prodBranch ("merged to prod") from any
// other base branch.
func RenderMerged(title, htmlURL, author, baseRef, prodBranch string) string {
	if baseRef == prodBranch {
		return fmt.Sprintf("🚀 PR merged to production (%s): <%s|%s> by %s", baseRef, htmlURL, title, author)
	}
	return fmt.Sprintf("✅ PR merged into %s: <%s|%s> by %s", baseRef, htmlURL, title, author)
}

// RenderAssessmentPosted builds the Slack notification text for the AI
// review/CI-failure comment being posted on a PR.
func RenderAssessmentPosted(title, htmlURL, commentURL string) string {
	return fmt.Sprintf("🤖 AI review posted on <%s|%s>: %s", htmlURL, title, commentURL)
}
