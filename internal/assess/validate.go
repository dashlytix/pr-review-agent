package assess

import (
	"regexp"
	"strconv"
	"strings"
)

var hunkHeader = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// ValidateAnchor enforces §6.1's diff-anchored-findings guardrail: a
// file/line citation is only left as a precise anchor if it falls within
// a real changed-line range in the diff this run actually captured. A
// claim the Action cannot verify against its own gathered context is
// downgraded here rather than posted as a confident, specific location.
func ValidateAnchor(req AssessmentRequest, a *Assessment) {
	changed := ChangedLines(req)
	validateAnchorAgainst(changed, a)
}

// ValidateAnchors applies the same guardrail across every finding a
// provider returned, computing the changed-line map once rather than
// re-parsing the diff per finding.
func ValidateAnchors(req AssessmentRequest, findings []Assessment) {
	changed := ChangedLines(req)
	for i := range findings {
		validateAnchorAgainst(changed, &findings[i])
	}
}

func validateAnchorAgainst(changed map[string]map[int]bool, a *Assessment) {
	if !a.Anchored {
		return
	}
	if a.File == "" || a.Line <= 0 {
		a.Anchored = false
		return
	}
	lines, ok := changed[a.File]
	if !ok || !lines[a.Line] {
		a.Anchored = false
	}
}

// ChangedLines maps each touched file to the set of new-side line numbers
// that were actually added/changed, by parsing the combined PR diff and
// any per-file patches supplied in req.Files. Both are consulted because
// gather may populate one, the other, or both depending on what the
// GitHub API returned for a given run.
func ChangedLines(req AssessmentRequest) map[string]map[int]bool {
	changed := parseUnifiedDiff(req.Diff)

	for name, patch := range req.Files {
		lines := parseHunks(patch)
		if len(lines) == 0 {
			continue
		}
		if changed[name] == nil {
			changed[name] = lines
		} else {
			for ln := range lines {
				changed[name][ln] = true
			}
		}
	}

	return changed
}

// parseUnifiedDiff walks a full multi-file "diff --git" style unified
// diff, tracking the current file via "+++ b/<path>" markers.
func parseUnifiedDiff(diff string) map[string]map[int]bool {
	result := make(map[string]map[int]bool)
	if diff == "" {
		return result
	}

	var currentFile string
	var newLine int
	inHunk := false

	for _, raw := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(raw, "diff --git "):
			// New file section starting: whatever hunk was open for the
			// previous file is done. Without this, the "index ..." and
			// "--- a/<path>" lines that precede the next "+++ b/<path>"
			// would otherwise still read as inHunk (stale from the prior
			// file's last hunk) and get miscounted as changed lines.
			inHunk = false
		case strings.HasPrefix(raw, "+++ "):
			currentFile = strings.TrimPrefix(raw, "+++ ")
			currentFile = strings.TrimPrefix(currentFile, "b/")
			currentFile = strings.TrimSpace(currentFile)
			inHunk = false
		case strings.HasPrefix(raw, "@@ "):
			if m := hunkHeader.FindStringSubmatch(raw); m != nil {
				newLine, _ = strconv.Atoi(m[1])
				inHunk = true
			} else {
				inHunk = false
			}
		case inHunk && currentFile != "" && currentFile != "/dev/null":
			if result[currentFile] == nil {
				result[currentFile] = make(map[int]bool)
			}
			switch {
			case strings.HasPrefix(raw, "+"):
				result[currentFile][newLine] = true
				newLine++
			case strings.HasPrefix(raw, "-"):
				// removed line: doesn't exist on the new side, no counter change
			default:
				newLine++
			}
		}
	}

	return result
}

// parseHunks parses a single file's patch body (as returned by the
// GitHub pulls/files "patch" field: hunks only, no "diff --git"/"+++"
// header lines) into the set of changed new-side line numbers.
func parseHunks(patch string) map[int]bool {
	result := make(map[int]bool)
	var newLine int
	inHunk := false

	for _, raw := range strings.Split(patch, "\n") {
		if strings.HasPrefix(raw, "@@ ") {
			if m := hunkHeader.FindStringSubmatch(raw); m != nil {
				newLine, _ = strconv.Atoi(m[1])
				inHunk = true
				continue
			}
			inHunk = false
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(raw, "+"):
			result[newLine] = true
			newLine++
		case strings.HasPrefix(raw, "-"):
		default:
			newLine++
		}
	}

	return result
}
