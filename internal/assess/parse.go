package assess

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrMalformed is returned when a provider's output could not be parsed
// into findings even after the bounded repair attempt (§7:
// "if that also fails, a minimal body-only comment is posted rather than
// the finding being silently dropped"). Callers should errors.Is against
// this to decide whether to fall back to a minimal comment.
var ErrMalformed = errors.New("assess: assessment malformed after repair attempt")

var fencedJSON = regexp.MustCompile(`(?s)` + "```" + `(?:json)?\s*(\[.*?\])\s*` + "```")
var fencedJSONObject = regexp.MustCompile(`(?s)` + "```" + `(?:json)?\s*(\{.*?\})\s*` + "```")

var validSeverity = map[string]bool{"P0": true, "P1": true, "P2": true, "P3": true, "nit": true}
var validConfidence = map[string]bool{"high": true, "medium": true, "low": true}

// ParseAssessments extracts and validates a JSON array of findings from
// raw LLM output. It tolerates the model wrapping the array in a
// markdown code fence, or emitting leading/trailing prose around it,
// since providers don't reliably honor "no prose" instructions.
//
// Exactly one "ci-failure" finding is required — that's the mandatory
// diagnosis every run produces. Any other category is additive; there
// can be zero of them.
func ParseAssessments(raw string) ([]Assessment, error) {
	candidate := extractJSONArray(raw)
	if candidate == "" {
		return nil, fmt.Errorf("assess: no JSON array found in response")
	}

	var findings []Assessment
	if err := json.Unmarshal([]byte(candidate), &findings); err != nil {
		return nil, fmt.Errorf("assess: invalid JSON: %w", err)
	}
	if len(findings) == 0 {
		return nil, fmt.Errorf("assess: empty findings array")
	}

	hasCIFailure := false
	for i := range findings {
		a := &findings[i]

		if !ValidCategories[a.Category] {
			return nil, fmt.Errorf("assess: invalid category %q", a.Category)
		}
		if a.Category == "ci-failure" {
			hasCIFailure = true
		}
		if !validSeverity[a.Severity] {
			return nil, fmt.Errorf("assess: invalid severity %q", a.Severity)
		}
		if !validConfidence[a.Confidence] {
			return nil, fmt.Errorf("assess: invalid confidence %q", a.Confidence)
		}
		if a.File == "" || a.Line <= 0 {
			a.Anchored = false
		}
	}
	if !hasCIFailure {
		return nil, fmt.Errorf("assess: no ci-failure finding in response")
	}

	return findings, nil
}

// ParseReview is ParseAssessments' counterpart for the plain PR-review
// path. The response is a JSON object, not a bare array: "summary" is
// mandatory (a description of the PR itself, not derivable from its
// findings), and "findings" may be empty -- that's the valid, common "no
// issues found" result. "ci-failure" is rejected as a finding category
// here since there's no CI failure being diagnosed.
func ParseReview(raw string) (ReviewResult, error) {
	candidate := extractJSONObject(raw)
	if candidate == "" {
		return ReviewResult{}, fmt.Errorf("assess: no JSON object found in response")
	}

	var result ReviewResult
	if err := json.Unmarshal([]byte(candidate), &result); err != nil {
		return ReviewResult{}, fmt.Errorf("assess: invalid JSON: %w", err)
	}
	if strings.TrimSpace(result.Summary) == "" {
		return ReviewResult{}, fmt.Errorf("assess: missing summary")
	}

	for i := range result.Findings {
		a := &result.Findings[i]

		if a.Category == "ci-failure" || !ValidCategories[a.Category] {
			return ReviewResult{}, fmt.Errorf("assess: invalid category %q", a.Category)
		}
		if !validSeverity[a.Severity] {
			return ReviewResult{}, fmt.Errorf("assess: invalid severity %q", a.Severity)
		}
		if !validConfidence[a.Confidence] {
			return ReviewResult{}, fmt.Errorf("assess: invalid confidence %q", a.Confidence)
		}
		if a.File == "" || a.Line <= 0 {
			a.Anchored = false
		}
	}

	return result, nil
}

// extractJSONArray pulls the first plausible JSON array out of raw text:
// a fenced block if present, otherwise the outermost [...] span.
func extractJSONArray(raw string) string {
	raw = strings.TrimSpace(raw)
	if m := fencedJSON.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	start := strings.Index(raw, "[")
	end := strings.LastIndex(raw, "]")
	if start == -1 || end == -1 || end < start {
		return ""
	}
	return raw[start : end+1]
}

// extractJSONObject is extractJSONArray's counterpart for the review
// path's object-shaped response: a fenced block if present, otherwise
// the outermost {...} span.
func extractJSONObject(raw string) string {
	raw = strings.TrimSpace(raw)
	if m := fencedJSONObject.FindStringSubmatch(raw); m != nil {
		return m[1]
	}
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start == -1 || end == -1 || end < start {
		return ""
	}
	return raw[start : end+1]
}
