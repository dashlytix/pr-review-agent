package post

import (
	"fmt"
	"strings"

	"github.com/dimension/ai-ci-agent/internal/provider"
)

// RenderAssessments builds the PR comment body for a successful run.
// findings always contains exactly one "ci-failure" entry (the mandatory
// diagnosis) plus zero or more additional review findings spotted in the
// same diff; all of them post as one comment, not one per finding.
// staleHeadSHA is empty unless the PR's head moved since context was
// gathered, in which case §6.3 requires the comment to say so explicitly
// and drop inline anchoring for every finding.
func RenderAssessments(findings []provider.Assessment, reviewedSHA, staleHeadSHA string) string {
	var b strings.Builder
	b.WriteString("### 🤖 AI CI Agent — investigation results\n\n")

	if staleHeadSHA != "" {
		b.WriteString(fmt.Sprintf(
			"> **Note:** this PR's head moved after context was gathered (reviewed `%s`, current head `%s`). Findings below are shown without inline file/line anchors.\n\n",
			short(reviewedSHA), short(staleHeadSHA),
		))
	}
	stale := staleHeadSHA != ""

	primary, extra := splitPrimary(findings)

	if primary != nil {
		b.WriteString(renderFinding(*primary, stale))
	}

	if len(extra) > 0 {
		b.WriteString("\n---\n**Other findings noticed in this diff**\n")
		for _, f := range extra {
			b.WriteString("\n")
			b.WriteString(renderFinding(f, stale))
		}
	}

	b.WriteString("\n")
	b.WriteString(marker(reviewedSHA))
	return b.String()
}

// splitPrimary pulls out the mandatory ci-failure finding (rendered
// first, on its own) from any additional findings. Falls back to
// treating the first entry as primary if — contrary to the §-mandated
// contract enforced in assess.ParseAssessments — no ci-failure finding
// is present, so rendering never has to handle a nil primary.
func splitPrimary(findings []provider.Assessment) (*provider.Assessment, []provider.Assessment) {
	for i := range findings {
		if findings[i].Category == "ci-failure" {
			primary := findings[i]
			rest := make([]provider.Assessment, 0, len(findings)-1)
			rest = append(rest, findings[:i]...)
			rest = append(rest, findings[i+1:]...)
			return &primary, rest
		}
	}
	if len(findings) > 0 {
		primary := findings[0]
		return &primary, findings[1:]
	}
	return nil, nil
}

func renderFinding(a provider.Assessment, stale bool) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("**Category:** %s\n", a.Category))
	b.WriteString(fmt.Sprintf("**Severity:** %s\n", a.Severity))
	b.WriteString(fmt.Sprintf("**Confidence:** %s\n", a.Confidence))

	if !stale && a.Anchored && a.File != "" {
		b.WriteString(fmt.Sprintf("**Location:** `%s:%d`\n", a.File, a.Line))
	} else if a.File != "" {
		b.WriteString(fmt.Sprintf("**Location (unanchored):** `%s`\n", a.File))
	} else {
		b.WriteString("**Location:** not identified\n")
	}

	b.WriteString("\n")
	b.WriteString(orNone(a.Comment))

	if strings.TrimSpace(a.SuggestedFix) != "" {
		b.WriteString("\n\n**Suggested fix:** ")
		b.WriteString(a.SuggestedFix)
	}
	b.WriteString("\n")
	return b.String()
}

// RenderReviewFindings builds the PR comment body for the plain
// PR-review path (pull_request opened/synchronize). Unlike
// RenderAssessments there is no mandatory primary finding — an empty
// slice is a valid, common outcome and renders as an explicit "no
// issues found" rather than an empty comment. Uses reviewMarker, not
// marker, so this comment's idempotency never collides with a
// CI-failure comment on the same commit.
func RenderReviewFindings(findings []provider.Assessment, sha string) string {
	var b strings.Builder
	b.WriteString("### 🤖 AI PR Review\n\n")

	if len(findings) == 0 {
		b.WriteString("No issues found in this diff.\n")
	} else {
		for i, f := range findings {
			if i > 0 {
				b.WriteString("\n---\n")
			}
			b.WriteString(renderFinding(f, false))
		}
	}

	b.WriteString("\n")
	b.WriteString(reviewMarker(sha))
	return b.String()
}

// RenderReviewFallback is RenderFallback's counterpart for the review
// path — there's no CI run/raw-logs link to point to here, so the
// message just names the outage.
func RenderReviewFallback(sha string) string {
	var b strings.Builder
	b.WriteString("### 🤖 AI PR Review\n\n")
	b.WriteString("The LLM provider was unavailable or timed out, so no automated review could be generated for this PR.\n\n")
	b.WriteString(reviewMarker(sha))
	return b.String()
}

// RenderReviewMinimal is RenderMinimal's counterpart for the review path,
// using reviewMarker so the comment is found by PostReview's idempotency
// check rather than Post's.
func RenderReviewMinimal(reason, sha string) string {
	var b strings.Builder
	b.WriteString("### 🤖 AI PR Review\n\n")
	b.WriteString(fmt.Sprintf("Unable to produce a full review for this PR: %s\n\n", reason))
	b.WriteString(reviewMarker(sha))
	return b.String()
}

// RenderFallback covers §7's "LLM provider unavailable or times out":
// post a fallback comment linking the raw logs and exit non-fatally.
func RenderFallback(runHTMLURL, sha string) string {
	var b strings.Builder
	b.WriteString("### 🤖 AI CI Agent\n\n")
	b.WriteString("The LLM provider was unavailable or timed out, so no automated investigation could be generated for this failure.\n\n")
	if runHTMLURL != "" {
		b.WriteString(fmt.Sprintf("Raw logs: %s\n\n", runHTMLURL))
	}
	b.WriteString(marker(sha))
	return b.String()
}

// RenderMinimal covers the other §7 fallback paths: rate limiting
// exhausted, or the assessment stayed malformed after the bounded
// repair attempt. reason is a short, human-readable explanation.
func RenderMinimal(reason, sha string) string {
	var b strings.Builder
	b.WriteString("### 🤖 AI CI Agent\n\n")
	b.WriteString(fmt.Sprintf("Unable to produce a full investigation for this failure: %s\n\n", reason))
	b.WriteString(marker(sha))
	return b.String()
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "_none provided_"
	}
	return s
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
