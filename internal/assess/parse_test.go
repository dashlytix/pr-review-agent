package assess

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseAssessments_PlainJSON(t *testing.T) {
	raw := `[{"file":"vehicles/garage.go","line":42,"category":"ci-failure","severity":"P1","comment":"nil owner","suggested_fix":"check g.owner","confidence":"high","anchored":true}]`

	findings, err := ParseAssessments(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}
	a := findings[0]
	if a.Category != "ci-failure" {
		t.Errorf("category = %q, want ci-failure", a.Category)
	}
	if a.File != "vehicles/garage.go" || a.Line != 42 {
		t.Errorf("file/line = %q:%d, want vehicles/garage.go:42", a.File, a.Line)
	}
	if a.Severity != "P1" || a.Confidence != "high" {
		t.Errorf("severity/confidence = %q/%q, want P1/high", a.Severity, a.Confidence)
	}
}

func TestParseAssessments_FencedJSON(t *testing.T) {
	raw := "Here is my assessment:\n```json\n[{\"file\":\"a.go\",\"line\":1,\"category\":\"ci-failure\",\"severity\":\"P2\",\"comment\":\"x\",\"suggested_fix\":\"\",\"confidence\":\"low\",\"anchored\":false}]\n```\nLet me know if you need more.\n"

	findings, err := ParseAssessments(raw)
	if err != nil {
		t.Fatalf("unexpected error extracting fenced JSON: %v", err)
	}
	if len(findings) != 1 || findings[0].File != "a.go" || findings[0].Severity != "P2" {
		t.Errorf("got %+v", findings)
	}
}

func TestParseAssessments_MultipleFindings(t *testing.T) {
	raw := `[
		{"file":"a.go","line":1,"category":"ci-failure","severity":"P1","comment":"the failure","suggested_fix":"","confidence":"high","anchored":true},
		{"file":"b.go","line":5,"category":"security","severity":"P0","comment":"a leaked secret","suggested_fix":"rotate it","confidence":"medium","anchored":true}
	]`

	findings, err := ParseAssessments(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("expected 2 findings, got %d: %+v", len(findings), findings)
	}
	if findings[0].Category != "ci-failure" || findings[1].Category != "security" {
		t.Errorf("categories = %q, %q; want ci-failure, security", findings[0].Category, findings[1].Category)
	}
}

func TestParseAssessments_RequiresACIFailureFinding(t *testing.T) {
	raw := `[{"file":"a.go","line":1,"category":"style","severity":"nit","comment":"x","suggested_fix":"","confidence":"low","anchored":false}]`
	if _, err := ParseAssessments(raw); err == nil {
		t.Fatal("expected an error when no ci-failure finding is present")
	}
}

func TestParseAssessments_InvalidCategory(t *testing.T) {
	raw := `[{"file":"a.go","line":1,"category":"ci-failure","severity":"P1","comment":"x","suggested_fix":"","confidence":"low","anchored":false},{"file":"a.go","line":1,"category":"performance-nonsense","severity":"P1","comment":"x","suggested_fix":"","confidence":"low","anchored":false}]`
	if _, err := ParseAssessments(raw); err == nil {
		t.Fatal("expected an error for an out-of-enum category")
	}
}

func TestParseAssessments_InvalidSeverity(t *testing.T) {
	raw := `[{"file":"a.go","line":1,"category":"ci-failure","severity":"critical","comment":"x","suggested_fix":"","confidence":"low","anchored":false}]`
	if _, err := ParseAssessments(raw); err == nil {
		t.Fatal("expected an error for an out-of-enum severity, got nil")
	}
}

func TestParseAssessments_InvalidConfidence(t *testing.T) {
	raw := `[{"file":"a.go","line":1,"category":"ci-failure","severity":"P1","comment":"x","suggested_fix":"","confidence":"certain","anchored":false}]`
	if _, err := ParseAssessments(raw); err == nil {
		t.Fatal("expected an error for an out-of-enum confidence, got nil")
	}
}

func TestParseAssessments_NoJSONFound(t *testing.T) {
	if _, err := ParseAssessments("I couldn't figure this one out, sorry."); err == nil {
		t.Fatal("expected an error when no JSON array is present")
	}
}

func TestParseAssessments_EmptyArray(t *testing.T) {
	if _, err := ParseAssessments("[]"); err == nil {
		t.Fatal("expected an error for an empty findings array")
	}
}

func TestParseAssessments_MissingFileForcesUnanchored(t *testing.T) {
	raw := `[{"file":"","line":0,"category":"ci-failure","severity":"P1","comment":"x","suggested_fix":"","confidence":"medium","anchored":true}]`
	findings, err := ParseAssessments(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if findings[0].Anchored {
		t.Error("anchored should be forced false when file/line are empty, regardless of what the model claimed")
	}
}

// ErrMalformed is a sentinel cmd/agent relies on via errors.Is to decide
// whether to post a minimal comment (§7). ClaudeProvider/OpenAIProvider
// wrap it with fmt.Errorf("%w: ...", ErrMalformed) — confirm that survives
// unwrapping the way errors.Is expects.
func TestErrMalformed_SurvivesWrapping(t *testing.T) {
	plain := errors.New("outer: " + ErrMalformed.Error())
	if errors.Is(plain, ErrMalformed) {
		t.Fatal("sanity check: a string-built error should NOT satisfy errors.Is without %w wrapping")
	}

	wrapped := fmt.Errorf("%w: some parse detail", ErrMalformed)
	if !errors.Is(wrapped, ErrMalformed) {
		t.Fatal("an error built with %w around ErrMalformed must satisfy errors.Is(err, ErrMalformed)")
	}
}

func TestParseReviewAssessments_EmptyArrayIsValid(t *testing.T) {
	findings, err := ParseReviewAssessments("[]")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 0 {
		t.Errorf("expected 0 findings, got %d", len(findings))
	}
}

func TestParseReviewAssessments_ValidFinding(t *testing.T) {
	raw := `[{"file":"a.go","line":1,"category":"correctness","severity":"P2","comment":"off-by-one","suggested_fix":"use <=","confidence":"medium","anchored":true}]`

	findings, err := ParseReviewAssessments(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 1 || findings[0].Category != "correctness" {
		t.Errorf("got %+v", findings)
	}
}

func TestParseReviewAssessments_RejectsCIFailureCategory(t *testing.T) {
	raw := `[{"file":"a.go","line":1,"category":"ci-failure","severity":"P1","comment":"x","suggested_fix":"","confidence":"low","anchored":false}]`
	if _, err := ParseReviewAssessments(raw); err == nil {
		t.Fatal("expected an error for a ci-failure category in the review path — there's no CI failure to diagnose here")
	}
}

func TestParseReviewAssessments_InvalidCategory(t *testing.T) {
	raw := `[{"file":"a.go","line":1,"category":"performance-nonsense","severity":"P1","comment":"x","suggested_fix":"","confidence":"low","anchored":false}]`
	if _, err := ParseReviewAssessments(raw); err == nil {
		t.Fatal("expected an error for an out-of-enum category")
	}
}

func TestParseReviewAssessments_NoJSONFound(t *testing.T) {
	if _, err := ParseReviewAssessments("I couldn't figure this one out, sorry."); err == nil {
		t.Fatal("expected an error when no JSON array is present")
	}
}

func TestExtractJSONArray_IgnoresSurroundingProse(t *testing.T) {
	raw := `Sure thing! [{"file":"x","line":1,"category":"ci-failure","severity":"P1","comment":"c","suggested_fix":"","confidence":"low","anchored":false}] Hope that helps.`
	got := extractJSONArray(raw)
	if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "]") {
		t.Fatalf("extractJSONArray did not isolate the array: %q", got)
	}
}
