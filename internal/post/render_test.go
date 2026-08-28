package post

import (
	"strings"
	"testing"

	"github.com/dimension/ai-ci-agent/internal/provider"
)

func TestRenderAssessmentReview_DiagnosisIsInDiagnosisSectionNotInlineComment(t *testing.T) {
	findings := []provider.Assessment{{
		File: "vehicles/garage.go", Line: 41, Category: "ci-failure",
		Severity: "P1", Comment: "nil owner check", SuggestedFix: "check g == nil first",
		Confidence: "high", Anchored: true,
	}}

	summary, comments := RenderAssessmentReview(findings, "abc1234", "")

	if !strings.Contains(summary, marker("abc1234")) {
		t.Error("summary must embed the marker for its own SHA, for §6.3 idempotency lookup")
	}
	if !strings.Contains(summary, "### Diagnosis") || !strings.Contains(summary, "nil owner check") {
		t.Errorf("expected a Diagnosis section carrying the ci-failure finding, got:\n%s", summary)
	}
	if !strings.Contains(summary, "| P1 | High | ci-failure |") {
		t.Errorf("expected a diagnosis table row with severity/confidence/category, got:\n%s", summary)
	}
	if len(comments) != 0 {
		t.Errorf("the mandatory ci-failure finding must never become an inline comment, got %+v", comments)
	}
}

func TestRenderAssessmentReview_AnchoredExtraFindingBecomesInlineComment(t *testing.T) {
	findings := []provider.Assessment{
		{File: "vehicles/garage.go", Line: 41, Category: "ci-failure", Severity: "P1", Comment: "nil owner check", Confidence: "high", Anchored: true},
		{File: "vehicles/other.go", Line: 7, Category: "security", Severity: "P0", Comment: "a hardcoded credential", Confidence: "medium", Anchored: true},
	}

	summary, comments := RenderAssessmentReview(findings, "abc1234", "")

	if len(comments) != 1 {
		t.Fatalf("len(comments) = %d, want 1", len(comments))
	}
	if comments[0].Path != "vehicles/other.go" || comments[0].Line != 7 {
		t.Errorf("comment = %+v, want it anchored at vehicles/other.go:7", comments[0])
	}
	if !strings.Contains(comments[0].Body, "hardcoded credential") || !strings.Contains(comments[0].Body, "SECURITY") {
		t.Errorf("comment body = %q, want the finding's text and its uppercased category", comments[0].Body)
	}
	if !strings.Contains(summary, "### Additional Findings") || !strings.Contains(summary, "hardcoded credential") {
		t.Errorf("summary = %q, want an Additional Findings table row for the extra finding", summary)
	}
	if !strings.Contains(summary, "1 critical") {
		t.Errorf("summary = %q, want the executive summary to count the extra finding as critical", summary)
	}
}

func TestRenderAssessmentReview_UnanchoredFindingStaysInTableOnly(t *testing.T) {
	findings := []provider.Assessment{
		{File: "a.go", Line: 1, Category: "ci-failure", Severity: "P1", Comment: "x", Confidence: "high", Anchored: true},
		{File: "vehicles/garage.go", Line: 999, Category: "style", Severity: "nit", Comment: "unclear naming", Confidence: "low", Anchored: false},
	}

	summary, comments := RenderAssessmentReview(findings, "abc1234", "")

	if len(comments) != 0 {
		t.Errorf("an unanchored finding must never become an inline comment, got %+v", comments)
	}
	if !strings.Contains(summary, "unclear naming") {
		t.Errorf("an unanchored finding must still be visible somewhere in the report, got:\n%s", summary)
	}
}

func TestRenderAssessmentReview_StaleHeadDropsAllAnchorsAndNotesBothSHAs(t *testing.T) {
	findings := []provider.Assessment{
		{File: "a.go", Line: 1, Category: "ci-failure", Severity: "P1", Comment: "x", Confidence: "high", Anchored: true},
		{File: "vehicles/garage.go", Line: 41, Category: "security", Severity: "P0", Comment: "nil owner check", Confidence: "high", Anchored: true},
	}

	summary, comments := RenderAssessmentReview(findings, "abc1234", "def5678")

	if len(comments) != 0 {
		t.Errorf("§6.3: a stale-head run must post zero inline comments, got %+v", comments)
	}
	if !strings.Contains(summary, "abc1234") || !strings.Contains(summary, "def5678") {
		t.Errorf("expected both the reviewed and current SHA to be called out, got:\n%s", summary)
	}
	if !strings.Contains(summary, marker("abc1234")) {
		t.Error("the marker must still key off the reviewed SHA, not the current head")
	}
}

func TestRenderAssessmentReview_RecommendationReflectsRisk(t *testing.T) {
	critical := []provider.Assessment{{Category: "ci-failure", Severity: "P0", Confidence: "high", Comment: "x"}}
	summary, _ := RenderAssessmentReview(critical, "abc1234", "")
	if !strings.Contains(summary, "### Recommendation") || !strings.Contains(summary, "Block merge") {
		t.Errorf("expected a block-merge recommendation for a P0 diagnosis, got:\n%s", summary)
	}

	warning := []provider.Assessment{{Category: "ci-failure", Severity: "P1", Confidence: "high", Comment: "x"}}
	summary, _ = RenderAssessmentReview(warning, "abc1234", "")
	if !strings.Contains(summary, "Review the findings above") {
		t.Errorf("expected a review-required recommendation for a P1-only diagnosis, got:\n%s", summary)
	}
}

func TestRenderFallback_LinksRunAndEmbedsNoMarker(t *testing.T) {
	body := RenderFallback("https://github.com/o/r/actions/runs/123")

	if !strings.Contains(body, "https://github.com/o/r/actions/runs/123") {
		t.Errorf("expected the raw run URL to be linked, got:\n%s", body)
	}
	if strings.Contains(body, "ai-ci-agent:marker") {
		t.Error("a degraded fallback post must not embed the marker -- it isn't a completed review, and embedding it would permanently block a later successful retry for this commit")
	}
}

func TestRenderMinimal_IncludesReasonAndNoMarker(t *testing.T) {
	body := RenderMinimal("the GitHub API rate limit was hit while gathering context")

	if !strings.Contains(body, "rate limit") {
		t.Errorf("expected the reason to be rendered, got:\n%s", body)
	}
	if strings.Contains(body, "ai-ci-agent:marker") {
		t.Error("a degraded minimal post must not embed the marker, for the same reason as RenderFallback")
	}
}

func TestRenderReviewReview_NoIssuesSaysSo(t *testing.T) {
	result := provider.ReviewResult{Summary: "Adds a health-check endpoint to the HTTP API."}
	summary, comments := RenderReviewReview(result, "abc1234", false)

	if !strings.Contains(summary, "### Executive Summary\n\nAdds a health-check endpoint") {
		t.Errorf("expected the Executive Summary section to lead the report, got:\n%s", summary)
	}
	if !strings.Contains(summary, "**Risk level:** Good") {
		t.Errorf("expected a Good risk level with zero findings, got:\n%s", summary)
	}
	if !strings.Contains(summary, "No issues were identified") {
		t.Errorf("expected an explicit no-issues message for an empty findings slice, got:\n%s", summary)
	}
	if !strings.Contains(summary, reviewMarker("abc1234")) {
		t.Error("expected the review marker to be embedded even when there are no findings")
	}
	if len(comments) != 0 {
		t.Errorf("no findings means no inline comments, got %+v", comments)
	}
}

func TestRenderReviewReview_NoSummaryOmitsSection(t *testing.T) {
	summary, _ := RenderReviewReview(provider.ReviewResult{}, "abc1234", false)
	if strings.Contains(summary, "### Executive Summary") {
		t.Errorf("expected no Executive Summary section when Summary is empty, got:\n%s", summary)
	}
}

func TestRenderReviewReview_ConflictingAddsWarning(t *testing.T) {
	summary, _ := RenderReviewReview(provider.ReviewResult{Summary: "x"}, "abc1234", true)
	if !strings.Contains(summary, "merge conflicts") {
		t.Errorf("expected a merge-conflict warning when conflicting is true, got:\n%s", summary)
	}
}

func TestRenderReviewReview_NotConflictingOmitsWarning(t *testing.T) {
	summary, _ := RenderReviewReview(provider.ReviewResult{Summary: "x"}, "abc1234", false)
	if strings.Contains(summary, "merge conflicts") {
		t.Errorf("expected no merge-conflict warning when conflicting is false, got:\n%s", summary)
	}
}

func TestRenderReviewReview_AnchoredFindingsBecomeInlineComments(t *testing.T) {
	result := provider.ReviewResult{
		Summary: "Adds a hardcoded-credential check and fixes an off-by-one in pagination.",
		Findings: []provider.Assessment{
			{File: "a.go", Line: 1, Category: "correctness", Severity: "P2", Comment: "off-by-one", Confidence: "medium", Anchored: true},
			{File: "b.go", Line: 7, Category: "security", Severity: "P0", Comment: "a hardcoded credential", Confidence: "high", Anchored: true},
		},
	}

	summary, comments := RenderReviewReview(result, "abc1234", false)

	if len(comments) != 2 {
		t.Fatalf("len(comments) = %d, want 2", len(comments))
	}
	if comments[0].Path != "a.go" || comments[0].Line != 1 || !strings.Contains(comments[0].Body, "off-by-one") {
		t.Errorf("comments[0] = %+v, want it anchored at a.go:1 with the off-by-one text", comments[0])
	}
	if comments[1].Path != "b.go" || comments[1].Line != 7 || !strings.Contains(comments[1].Body, "hardcoded credential") {
		t.Errorf("comments[1] = %+v, want it anchored at b.go:7 with the credential text", comments[1])
	}
	if !strings.Contains(summary, "### Findings") {
		t.Errorf("summary = %q, want a Findings section", summary)
	}
	if !strings.Contains(summary, "**Risk level:** Critical") {
		t.Errorf("summary = %q, want a Critical risk level since a P0 finding is present", summary)
	}
	if !strings.Contains(summary, "Block merge") {
		t.Errorf("summary = %q, want a block-merge recommendation for a Critical risk level", summary)
	}
}

func TestOverallImpact(t *testing.T) {
	tests := []struct {
		name      string
		findings  []provider.Assessment
		wantEmoji string
		wantLabel string
	}{
		{"no findings", nil, "🟢", "Good"},
		{"nit only", []provider.Assessment{{Severity: "nit"}}, "🟢", "Good"},
		{"P3 only, no P0", []provider.Assessment{{Severity: "P3"}}, "🟡", "Warning"},
		{"P1 without P0", []provider.Assessment{{Severity: "P1"}}, "🟡", "Warning"},
		{"P0 present", []provider.Assessment{{Severity: "P3"}, {Severity: "P0"}}, "🔴", "Critical"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			emoji, label := OverallImpact(tt.findings)
			if emoji != tt.wantEmoji || label != tt.wantLabel {
				t.Errorf("OverallImpact(%+v) = (%q, %q), want (%q, %q)", tt.findings, emoji, label, tt.wantEmoji, tt.wantLabel)
			}
		})
	}
}

// A completed CI-failure review (marker) and a completed plain-review
// review (reviewMarker) on the same commit SHA must not collide in
// findReviewByMarker's substring search, or one path's idempotency check
// would wrongly report the other's review as already posted.
func TestMarkerAndReviewMarker_NeverCollide(t *testing.T) {
	sha := "abc1234"
	ciFinding := []provider.Assessment{{Category: "ci-failure", Severity: "P1", Confidence: "high", Comment: "x"}}
	ciBody, _ := RenderAssessmentReview(ciFinding, sha, "")
	reviewBody, _ := RenderReviewReview(provider.ReviewResult{Summary: "x"}, sha, false)

	if strings.Contains(ciBody, reviewMarker(sha)) {
		t.Error("a CI-failure review must not embed the review marker")
	}
	if strings.Contains(reviewBody, marker(sha)) {
		t.Error("a plain-review review must not embed the CI-failure marker")
	}
}

// A degraded post (provider outage, rate limit, malformed output) must
// never embed either marker -- see RenderReviewFallback's doc comment for
// why a degraded post has to stay retryable rather than sealing the
// commit against a later successful attempt.
func TestDegradedRenders_EmbedNoMarkerEither(t *testing.T) {
	sha := "abc1234"
	for name, body := range map[string]string{
		"RenderFallback":       RenderFallback("https://x"),
		"RenderMinimal":        RenderMinimal("reason"),
		"RenderReviewFallback": RenderReviewFallback(),
		"RenderReviewMinimal":  RenderReviewMinimal("reason"),
	} {
		if strings.Contains(body, marker(sha)) || strings.Contains(body, reviewMarker(sha)) || strings.Contains(body, "ai-ci-agent:") {
			t.Errorf("%s = %q, want no marker of any kind embedded", name, body)
		}
	}
}

func TestMarker_DiffersBySHA(t *testing.T) {
	if marker("abc1234") == marker("def5678") {
		t.Error("markers for different SHAs must differ, or idempotency lookup would collide across commits")
	}
	if marker("abc1234") != marker("abc1234") {
		t.Error("marker must be deterministic for the same SHA")
	}
}

func TestSeverityBucket(t *testing.T) {
	tests := []struct{ severity, want string }{
		{"P0", "critical"}, {"P1", "critical"},
		{"P2", "warning"}, {"P3", "warning"},
		{"nit", "nit"}, {"", "nit"},
	}
	for _, tt := range tests {
		if got := severityBucket(tt.severity); got != tt.want {
			t.Errorf("severityBucket(%q) = %q, want %q", tt.severity, got, tt.want)
		}
	}
}

// A finding's File field is free-form LLM output (internal/assess/parse.go
// only validates Category/Severity/Confidence, never File's content) --
// unlike Category/Severity/Confidence, a stray "|" or newline in File must
// not be allowed to corrupt the Location column and shift every other
// column in that table row.
func TestRenderFindingsTable_SanitizesFileInLocationColumn(t *testing.T) {
	findings := []provider.Assessment{
		{File: "a.go | b.go\nc.go", Line: 5, Category: "correctness", Severity: "P2", Comment: "x", Confidence: "medium", Anchored: false},
	}
	table, _, _ := renderFindingsTable(findings, true)

	rows := strings.Split(strings.TrimRight(table, "\n"), "\n")
	if len(rows) != 3 {
		t.Fatalf("table = %q, want exactly 3 lines (header, delimiter, one data row) -- a raw newline in File must not split it into more", table)
	}
	dataRow := rows[2]
	if unescaped := strings.ReplaceAll(dataRow, `\|`, ""); strings.Count(unescaped, "|") != 7 {
		t.Errorf("data row = %q, want exactly 7 unescaped pipes (6 columns), got %d", dataRow, strings.Count(unescaped, "|"))
	}
	if strings.Contains(table, "a.go | b.go") {
		t.Errorf("table = %q, want the literal pipe in File escaped, not left to split the row", table)
	}
	if !strings.Contains(table, `a.go \| b.go`) {
		t.Errorf("table = %q, want the File field's pipe escaped as \\|", table)
	}
}

func TestTableCell_EscapesPipesAndFoldsNewlines(t *testing.T) {
	got := tableCell("a | b\nc")
	if strings.Contains(got, "\n") {
		t.Errorf("tableCell(%q) = %q, want no raw newline (would break the table row)", "a | b\nc", got)
	}
	if !strings.Contains(got, `\|`) {
		t.Errorf("tableCell(%q) = %q, want the literal pipe escaped", "a | b\nc", got)
	}
}
