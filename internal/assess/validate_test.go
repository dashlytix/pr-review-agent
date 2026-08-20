package assess

import "testing"

const sampleDiff = `diff --git a/vehicles/garage.go b/vehicles/garage.go
index 1111111..2222222 100644
--- a/vehicles/garage.go
+++ b/vehicles/garage.go
@@ -38,7 +38,7 @@ func NewGarage(owner *Person) *Garage {
 }

 func (g *Garage) Owner() string {
-	if g.owner == nil {
+	if g == nil {
 		return ""
 	}
 	return g.owner.Name
diff --git a/vehicles/other.go b/vehicles/other.go
index 3333333..4444444 100644
--- a/vehicles/other.go
+++ b/vehicles/other.go
@@ -5,3 +5,4 @@ package vehicles

 func Noop() {}
+func Noop2() {}
`

func TestChangedLines_ParsesMultiFileDiff(t *testing.T) {
	req := AssessmentRequest{Diff: sampleDiff}
	changed := ChangedLines(req)

	// The hunk starts at new-side line 38 ("}"); walking context lines
	// (38, 39, 40) then the removed line (no new-side line consumed)
	// puts the added "+	if g == nil {" line at new-side line 41.
	if !changed["vehicles/garage.go"][41] {
		t.Errorf("expected vehicles/garage.go:41 to be marked changed, got %v", changed["vehicles/garage.go"])
	}

	// Regression check for the diff-header leak bug: the "index ...",
	// "--- a/vehicles/other.go" lines between the two files must not have
	// been absorbed into garage.go's changed-line set while inHunk was
	// stale from garage.go's own last hunk.
	if len(changed["vehicles/garage.go"]) != 1 {
		t.Errorf("garage.go should have exactly one changed line, got %v", changed["vehicles/garage.go"])
	}

	// other.go's hunk starts at new-side line 5; two context lines (5, 6)
	// put the added "+func Noop2() {}" line at new-side line 7.
	if !changed["vehicles/other.go"][7] {
		t.Errorf("expected vehicles/other.go:7 to be marked changed, got %v", changed["vehicles/other.go"])
	}
}

func TestValidateAnchor_AcceptsRealAnchor(t *testing.T) {
	req := AssessmentRequest{Diff: sampleDiff}
	a := &Assessment{File: "vehicles/garage.go", Line: 41, Anchored: true}

	ValidateAnchor(req, a)

	if !a.Anchored {
		t.Error("a genuinely changed file/line should remain anchored")
	}
}

func TestValidateAnchor_DowngradesLineOutsideDiff(t *testing.T) {
	req := AssessmentRequest{Diff: sampleDiff}
	a := &Assessment{File: "vehicles/garage.go", Line: 999, Anchored: true}

	ValidateAnchor(req, a)

	if a.Anchored {
		t.Error("a line outside the captured diff must be downgraded to unanchored (§6.1)")
	}
}

func TestValidateAnchor_DowngradesUnknownFile(t *testing.T) {
	req := AssessmentRequest{Diff: sampleDiff}
	a := &Assessment{File: "not/in/the/diff.go", Line: 1, Anchored: true}

	ValidateAnchor(req, a)

	if a.Anchored {
		t.Error("a file the diff never touched must be downgraded to unanchored (§6.1)")
	}
}

func TestValidateAnchor_FallsBackToPerFilePatch(t *testing.T) {
	// No combined diff, only a per-file patch (as gather.fetchFiles alone
	// would produce if the combined diff fetch failed but the files
	// endpoint succeeded).
	req := AssessmentRequest{
		Files: map[string]string{
			"src/quotes/pricing.ts": "@@ -12,7 +12,7 @@ export function applyDiscount(total: number, code: string): number {\n }\n \n export function finalPrice(total: number, code: string): number {\n-  return applyDiscount(total, discountPercent(code));\n+  return applyDiscount(total, code);\n }\n",
		},
	}
	a := &Assessment{File: "src/quotes/pricing.ts", Line: 15, Anchored: true}

	ValidateAnchor(req, a)

	if !a.Anchored {
		t.Error("a line changed in a per-file patch should anchor even without a combined diff")
	}
}

func TestValidateAnchors_AppliesToEveryFinding(t *testing.T) {
	req := AssessmentRequest{Diff: sampleDiff}
	findings := []Assessment{
		{File: "vehicles/garage.go", Line: 41, Anchored: true},  // real anchor
		{File: "vehicles/garage.go", Line: 999, Anchored: true}, // outside the diff
		{File: "vehicles/other.go", Line: 7, Anchored: true},    // real anchor, different file
	}

	ValidateAnchors(req, findings)

	if !findings[0].Anchored {
		t.Error("finding 0 should remain anchored")
	}
	if findings[1].Anchored {
		t.Error("finding 1 should be downgraded — line 999 is outside the diff")
	}
	if !findings[2].Anchored {
		t.Error("finding 2 should remain anchored")
	}
}
