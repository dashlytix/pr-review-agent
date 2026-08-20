package gather

import (
	"regexp"
	"strings"
)

// failurePatterns are language-agnostic-in-code but tuned to the four
// target languages validated by the eval harness (§9): Go, Rust,
// TypeScript, SQL. This is a heuristic pre-filter, not the assessment
// itself — it just points the LLM at the signal buried in a long log.
var failurePatterns = []*regexp.Regexp{
	// Go
	regexp.MustCompile(`^--- FAIL:`),
	regexp.MustCompile(`^FAIL\b`),
	regexp.MustCompile(`^panic:`),
	regexp.MustCompile(`\.go:\d+:\d+:`),
	regexp.MustCompile(`^# [\w./-]+$`), // go build package header preceding compile errors

	// Rust
	regexp.MustCompile(`^error(\[E\d+\])?:`),
	regexp.MustCompile(`^\s*-->\s`),
	regexp.MustCompile(`^thread '.*' panicked at`),
	regexp.MustCompile(`^test result: FAILED`),

	// TypeScript / JS test runners
	regexp.MustCompile(`error TS\d+:`),
	regexp.MustCompile(`^\s*✕\s`),
	regexp.MustCompile(`^\s*×\s`),
	regexp.MustCompile(`AssertionError`),
	regexp.MustCompile(`^\s*FAIL\s`),

	// SQL / migrations
	regexp.MustCompile(`(?i)ERROR:\s+syntax error`),
	regexp.MustCompile(`(?i)ERROR:\s+relation .* does not exist`),
	regexp.MustCompile(`(?i)SQLSTATE\[`),
	regexp.MustCompile(`(?i)migration failed`),
}

const (
	maxExtractedLines = 200
	contextAfter      = 2
)

// ExtractFailures scans a log tail for lines matching known failure
// signatures across the target languages and returns them (with a
// couple of trailing context lines each) so the prompt can lead with
// signal instead of the full log.
func ExtractFailures(logTail string) string {
	lines := strings.Split(logTail, "\n")
	var out []string
	seen := make(map[int]bool)

	for i, line := range lines {
		matched := false
		for _, re := range failurePatterns {
			if re.MatchString(line) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		for j := i; j <= i+contextAfter && j < len(lines); j++ {
			if !seen[j] {
				seen[j] = true
				out = append(out, lines[j])
			}
		}
		if len(out) >= maxExtractedLines {
			break
		}
	}

	return strings.Join(out, "\n")
}
