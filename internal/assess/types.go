// Package assess owns the prompt sent to an LLM provider and the parsing
// (with one bounded repair attempt, §7) of what comes back. It is the one
// place prompt wording and the JSON contract live, so ClaudeProvider and
// OpenAIProvider don't each carry their own copy.
//
// The Assessment/AssessmentRequest struct definitions live here rather
// than in internal/provider to avoid an import cycle: both concrete
// providers need to call BuildPrompt/ParseAssessment, so provider must
// import assess, not the other way around. internal/provider re-exports
// these two types via type aliases so callers can keep writing
// provider.Assessment per §4.2 without knowing about the split.
package assess

// AssessmentRequest carries everything gathered for one CI failure. The
// same gathered context (log tail, diff, files) backs both the mandatory
// CI-failure diagnosis and any additional review findings spotted in the
// diff — there's no separate gather step for the two.
type AssessmentRequest struct {
	LogTail     string
	FailedTests string
	Diff        string
	Files       map[string]string
}

// Assessment is one structured finding. A single run returns a slice of
// these: always exactly one with Category "ci-failure" (the diagnosis of
// why the trigger failed), plus zero or more with other categories for
// additional issues spotted in the diff along the way — correctness,
// security, style, performance. The shape reuses the PR review agent's
// finding schema per §4.4 rather than inventing a parallel one, which is
// what makes folding PR-review-style findings into the same struct a
// natural extension instead of a new type.
type Assessment struct {
	File         string `json:"file"`
	Line         int    `json:"line"`
	Category     string `json:"category"` // ci-failure | correctness | security | style | performance
	Severity     string `json:"severity"` // P0 | P1 | P2 | P3 | nit
	Comment      string `json:"comment"`  // the likely cause, in prose
	SuggestedFix string `json:"suggested_fix"`
	Confidence   string `json:"confidence"` // high | medium | low
	Anchored     bool   `json:"anchored"`   // true only if file/line fall inside the captured diff/log
}

// ValidCategories is the fixed set of categories a finding may declare.
var ValidCategories = map[string]bool{
	"ci-failure":  true,
	"correctness": true,
	"security":    true,
	"style":       true,
	"performance": true,
}
