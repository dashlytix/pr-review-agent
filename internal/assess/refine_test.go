package assess

import "testing"

func TestRefineAssessments_KeepsGroundedFindingsAndDropsWeakNoise(t *testing.T) {
	req := AssessmentRequest{
		Diff: sampleDiff,
		Files: map[string]string{
			"vehicles/garage.go": sampleDiff,
		},
	}

	findings := []Assessment{
		{
			File: "vehicles/garage.go", Line: 41, Category: "ci-failure",
			Severity: "P1", Comment: "nil owner check", Confidence: "high", Anchored: true,
		},
		{
			File: "vehicles/garage.go", Line: 41, Category: "ci-failure",
			Severity: "P1", Comment: "duplicate diagnosis", Confidence: "high", Anchored: true,
		},
		{
			File: "vehicles/garage.go", Line: 41, Category: "correctness",
			Severity: "P2", Comment: "nil owner check needs a stronger guard", SuggestedFix: "check g == nil first", Confidence: "high", Anchored: false,
		},
		{
			File: "vehicles/other.go", Line: 999, Category: "style",
			Severity: "nit", Comment: "generic note", Confidence: "low", Anchored: false,
		},
	}

	got := RefineAssessments(req, findings)

	if len(got) != 2 {
		t.Fatalf("expected 2 surviving findings, got %d: %+v", len(got), got)
	}
	if got[0].Category != "ci-failure" {
		t.Fatalf("expected the ci-failure diagnosis first, got %+v", got[0])
	}
	if got[1].Category != "correctness" || got[1].File != "vehicles/garage.go" {
		t.Fatalf("expected the grounded secondary finding to survive, got %+v", got[1])
	}
}
