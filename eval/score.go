package eval

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dimension/ai-ci-agent/internal/provider"
)

var severityRank = map[string]int{"P0": 0, "P1": 1, "P2": 2, "P3": 3, "nit": 4}

// Result is one fixture's scored outcome. Findings is everything the
// provider returned (the mandatory ci-failure diagnosis plus any extra
// review findings); scoring below only judges the ci-failure one against
// the fixture's known answer — there's no "known answer" for whatever
// extra correctness/security/style findings a model happens to surface.
type Result struct {
	Fixture  Fixture
	Findings []provider.Assessment
	Err      error

	CauseMatch  bool // §9: "does the stated cause match the known cause" (keyword proxy)
	SeverityOK  bool // severity is at least as urgent as the fixture's MinSeverity
	AnchorValid bool // anchored claims actually anchor within the diff/log (§6.1)
}

// Score scores one provider response against its fixture's known answer.
func Score(f Fixture, findings []provider.Assessment, err error) Result {
	r := Result{Fixture: f, Findings: findings, Err: err}
	if err != nil {
		return r
	}

	primary := ciFailureFinding(findings)
	if primary == nil {
		r.Err = fmt.Errorf("no ci-failure finding in provider response")
		return r
	}

	comment := strings.ToLower(primary.Comment)
	for _, kw := range f.ExpectedCauseKeywords {
		if strings.Contains(comment, strings.ToLower(kw)) {
			r.CauseMatch = true
			break
		}
	}

	if f.MinSeverity == "" {
		r.SeverityOK = true
	} else if got, ok1 := severityRank[primary.Severity]; ok1 {
		if want, ok2 := severityRank[f.MinSeverity]; ok2 {
			r.SeverityOK = got <= want // lower rank number = more urgent
		}
	}

	// If the model claimed an anchor, ValidateAnchor (already applied
	// inside the provider) should have left it Anchored=true only when
	// it truly falls inside the diff/log. We can't re-derive "should
	// have been anchored" from a fixture alone, so this just checks the
	// claim is internally consistent (an anchored claim has both a file
	// and a positive line).
	r.AnchorValid = !primary.Anchored || (primary.File != "" && primary.Line > 0)

	return r
}

// ciFailureFinding returns the mandatory ci-failure finding out of a
// provider's response, or nil if the provider somehow omitted it
// (contrary to the contract assess.ParseAssessments enforces).
func ciFailureFinding(findings []provider.Assessment) *provider.Assessment {
	for i := range findings {
		if findings[i].Category == "ci-failure" {
			return &findings[i]
		}
	}
	return nil
}

// Summary aggregates results overall and per language.
type Summary struct {
	Total        int
	CauseMatches int
	SeverityOK   int
	AnchorValid  int
	Errors       int
	PerLanguage  map[string]*languageStats
}

type languageStats struct {
	Total, CauseMatches int
}

func Summarize(results []Result) Summary {
	s := Summary{PerLanguage: make(map[string]*languageStats)}
	for _, r := range results {
		s.Total++
		lang := r.Fixture.Language
		if s.PerLanguage[lang] == nil {
			s.PerLanguage[lang] = &languageStats{}
		}
		s.PerLanguage[lang].Total++

		if r.Err != nil {
			s.Errors++
			continue
		}
		if r.CauseMatch {
			s.CauseMatches++
			s.PerLanguage[lang].CauseMatches++
		}
		if r.SeverityOK {
			s.SeverityOK++
		}
		if r.AnchorValid {
			s.AnchorValid++
		}
	}
	return s
}

// Print writes a human-readable report to the given writer-like printf
// function (kept as a plain function so cmd/evalrun can pass fmt.Printf
// without this package importing io).
func (s Summary) Print(printf func(format string, args ...interface{})) {
	printf("Evaluated %d fixture(s), %d error(s)\n", s.Total, s.Errors)
	printf("  cause match:   %d/%d\n", s.CauseMatches, s.Total)
	printf("  severity ok:   %d/%d\n", s.SeverityOK, s.Total)
	printf("  anchor valid:  %d/%d\n", s.AnchorValid, s.Total)

	langs := make([]string, 0, len(s.PerLanguage))
	for l := range s.PerLanguage {
		langs = append(langs, l)
	}
	sort.Strings(langs)
	printf("Per-language cause match:\n")
	for _, l := range langs {
		ls := s.PerLanguage[l]
		printf("  %-12s %d/%d\n", l, ls.CauseMatches, ls.Total)
	}
}

// CauseMatchRate is the primary aggregate quality signal named in §8:
// "Track eval score trend over time (§9) as the primary quality signal."
func (s Summary) CauseMatchRate() float64 {
	if s.Total == 0 {
		return 0
	}
	return float64(s.CauseMatches) / float64(s.Total)
}

func (s Summary) String() string {
	return fmt.Sprintf("cause-match rate: %.0f%% (%d/%d)", s.CauseMatchRate()*100, s.CauseMatches, s.Total)
}
