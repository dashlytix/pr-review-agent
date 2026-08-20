// Command evalrun is the §9 evaluation harness CLI: run every fixture in
// eval/fixtures through the configured provider and report cause-match,
// severity, and anchor-validity scores, overall and per language.
//
// Usage:
//
//	LLM_API_KEY=sk-... go run ./eval/cmd/evalrun \
//	    -fixtures eval/fixtures -provider claude -min-score 0.7
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/dimension/ai-ci-agent/eval"
	"github.com/dimension/ai-ci-agent/internal/provider"
)

func main() {
	fixturesDir := flag.String("fixtures", "eval/fixtures", "directory of fixture JSON files")
	providerName := flag.String("provider", "claude", "llm provider to evaluate (claude | openai)")
	minScore := flag.Float64("min-score", 0.0, "exit non-zero if the cause-match rate falls below this (0-1)")
	verbose := flag.Bool("verbose", false, "print each fixture's full assessment, not just pass/fail")
	flag.Parse()

	apiKey := os.Getenv("LLM_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "evalrun: LLM_API_KEY is not set")
		os.Exit(1)
	}

	p, err := provider.Get(*providerName, apiKey)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evalrun: %v\n", err)
		os.Exit(1)
	}

	fixtures, err := eval.LoadFixtures(*fixturesDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evalrun: %v\n", err)
		os.Exit(1)
	}
	if len(fixtures) == 0 {
		fmt.Fprintf(os.Stderr, "evalrun: no fixtures found in %s\n", *fixturesDir)
		os.Exit(1)
	}

	var results []eval.Result
	for _, f := range fixtures {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		findings, err := p.Assess(ctx, f.Request())
		cancel()

		r := eval.Score(f, findings, err)
		results = append(results, r)

		status := "ok"
		if err != nil {
			status = "ERROR: " + err.Error()
		} else if r.Err != nil {
			status = "ERROR: " + r.Err.Error()
		} else if !r.CauseMatch {
			status = "cause mismatch"
		}
		extra := len(findings) - 1 // -1 for the mandatory ci-failure finding
		if extra > 0 {
			status += fmt.Sprintf(" (+%d other finding(s))", extra)
		}
		fmt.Printf("[%s/%s] %s\n", f.Language, f.ID, status)

		if *verbose && err == nil {
			for _, a := range findings {
				fmt.Printf("    [%s] %s:%d (anchored=%v)\n", a.Category, a.File, a.Line, a.Anchored)
				fmt.Printf("        severity:      %s\n", a.Severity)
				fmt.Printf("        confidence:    %s\n", a.Confidence)
				fmt.Printf("        comment:       %s\n", a.Comment)
				fmt.Printf("        suggested_fix: %s\n", a.SuggestedFix)
			}
		}
	}

	summary := eval.Summarize(results)
	fmt.Println()
	summary.Print(func(format string, args ...interface{}) { fmt.Printf(format, args...) })
	fmt.Println(summary)

	if summary.CauseMatchRate() < *minScore {
		fmt.Fprintf(os.Stderr, "evalrun: cause-match rate %.2f below threshold %.2f\n", summary.CauseMatchRate(), *minScore)
		os.Exit(1)
	}
}
