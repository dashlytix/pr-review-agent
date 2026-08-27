package assess

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// RefineAssessments applies a second, deterministic verification pass to
// a provider's candidate findings before they are posted. The pass keeps
// the mandatory ci-failure diagnosis, removes duplicates, and drops
// weakly supported secondary findings that are not grounded in the
// gathered diff/log context.
func RefineAssessments(req AssessmentRequest, findings []Assessment) []Assessment {
	if len(findings) == 0 {
		return nil
	}

	ValidateAnchors(req, findings)

	ctx := newRefinementContext(req)
	seen := make(map[string]struct{}, len(findings))
	refined := make([]Assessment, 0, len(findings))
	seenPrimary := false

	for _, a := range findings {
		if a.Category == "ci-failure" {
			if seenPrimary {
				continue
			}
			seenPrimary = true
			refined = append(refined, a)
			continue
		}

		if !shouldKeepAssessment(ctx, a) {
			continue
		}

		key := assessmentKey(a)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		refined = append(refined, a)
	}

	sort.SliceStable(refined, func(i, j int) bool {
		return assessmentLess(refined[i], refined[j])
	})

	return refined
}

type refinementContext struct {
	haystack string
}

func newRefinementContext(req AssessmentRequest) refinementContext {
	var b strings.Builder

	writeLower := func(s string) {
		if s == "" {
			return
		}
		b.WriteString(strings.ToLower(s))
		b.WriteString("\n")
	}

	writeLower(req.LogTail)
	writeLower(req.FailedTests)
	writeLower(req.Diff)
	for name, patch := range req.Files {
		writeLower(name)
		writeLower(patch)
	}

	return refinementContext{haystack: b.String()}
}

func shouldKeepAssessment(ctx refinementContext, a Assessment) bool {
	if strings.TrimSpace(a.Comment) == "" && strings.TrimSpace(a.SuggestedFix) == "" {
		return false
	}

	// A finding with a real diff anchor is already grounded by
	// ValidateAnchors; keep it unless it is a duplicate of a previous
	// finding.
	if a.Anchored && a.File != "" && a.Line > 0 {
		return true
	}

	// Unanchored findings need to earn their keep by matching the
	// evidence we gathered. The goal is to keep confident, actionable
	// statements while dropping speculative noise.
	score := evidenceScore(ctx.haystack, a.Comment+" "+a.SuggestedFix)
	if score >= 2 {
		return true
	}
	if score >= 1 && (a.File != "" || a.Line > 0) && a.Confidence == "high" {
		return true
	}
	if a.Severity == "P0" && score >= 1 {
		return true
	}
	return false
}

func evidenceScore(haystack string, probe string) int {
	score := 0
	for _, token := range splitEvidenceTokens(probe) {
		if strings.Contains(haystack, token) {
			score++
		}
	}
	return score
}

func splitEvidenceTokens(s string) []string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return nil
	}
	return strings.FieldsFunc(s, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-')
	})
}

func assessmentKey(a Assessment) string {
	return strings.Join([]string{
		a.File,
		fmt.Sprint(a.Line),
		a.Category,
		a.Severity,
		strings.TrimSpace(strings.ToLower(a.Comment)),
		strings.TrimSpace(strings.ToLower(a.SuggestedFix)),
		a.Confidence,
		fmt.Sprint(a.Anchored),
	}, "\x00")
}

func assessmentLess(a, b Assessment) bool {
	if categoryRank(a.Category) != categoryRank(b.Category) {
		return categoryRank(a.Category) < categoryRank(b.Category)
	}
	if severityRank(a.Severity) != severityRank(b.Severity) {
		return severityRank(a.Severity) < severityRank(b.Severity)
	}
	if anchoredRank(a.Anchored) != anchoredRank(b.Anchored) {
		return anchoredRank(a.Anchored) < anchoredRank(b.Anchored)
	}
	if confidenceRank(a.Confidence) != confidenceRank(b.Confidence) {
		return confidenceRank(a.Confidence) < confidenceRank(b.Confidence)
	}
	if a.File != b.File {
		return a.File < b.File
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Comment < b.Comment
}

func categoryRank(category string) int {
	switch category {
	case "ci-failure":
		return 0
	case "security":
		return 1
	case "correctness":
		return 2
	case "performance":
		return 3
	case "style":
		return 4
	default:
		return 5
	}
}

func severityRank(severity string) int {
	switch severity {
	case "P0":
		return 0
	case "P1":
		return 1
	case "P2":
		return 2
	case "P3":
		return 3
	case "nit":
		return 4
	default:
		return 5
	}
}

func anchoredRank(anchored bool) int {
	if anchored {
		return 0
	}
	return 1
}

func confidenceRank(confidence string) int {
	switch confidence {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	default:
		return 3
	}
}
