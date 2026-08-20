// Package eval implements §9's evaluation harness: run the agent's
// assessment logic against a fixed set of past CI failures with known
// root causes, and score the result. It exists to catch regressions
// whenever the prompt or provider changes (§1 Decision), since there is
// no persistent thread of past runs to compare against otherwise.
package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/dimension/ai-ci-agent/internal/assess"
)

// Fixture is one recorded past CI failure with its real, known root
// cause. §9 target: 20-30 fixtures to start, covering all four target
// languages so a prompt change can't silently regress on a language
// that's rare in whichever repo happens to be piloting this.
type Fixture struct {
	ID       string `json:"id"`
	Language string `json:"language"` // go | rust | typescript | sql

	LogTail string            `json:"log_tail"`
	Diff    string            `json:"diff"`
	Files   map[string]string `json:"files"`

	// ExpectedCauseKeywords: the assessment's "comment" field is scored
	// as a cause match if it contains at least one of these, case
	// insensitively. This is a keyword proxy for "does the stated cause
	// match the known cause" (§9) — a real yes/partial/no judgment call
	// still belongs to whoever curates and reviews the eval run, per the
	// open question in §11 on who owns and curates the eval dataset.
	ExpectedCauseKeywords []string `json:"expected_cause_keywords"`

	// MinSeverity is the lowest acceptable severity ("P0" > "P1" > ... >
	// "nit" in urgency). Per §6.1, a CI failure is at minimum P1.
	MinSeverity string `json:"min_severity"`
}

func (f Fixture) Request() assess.AssessmentRequest {
	return assess.AssessmentRequest{
		LogTail:     f.LogTail,
		FailedTests: "", // left for the harness to extract via internal/gather.ExtractFailures if desired
		Diff:        f.Diff,
		Files:       f.Files,
	}
}

// LoadFixtures reads every *.json file in dir, sorted by filename so a
// run's output order is stable across invocations.
func LoadFixtures(dir string) ([]Fixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("eval: read fixtures dir: %w", err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".json" {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	fixtures := make([]Fixture, 0, len(names))
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("eval: read %s: %w", name, err)
		}
		var f Fixture
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("eval: parse %s: %w", name, err)
		}
		if f.ID == "" {
			f.ID = name
		}
		fixtures = append(fixtures, f)
	}
	return fixtures, nil
}
