package post

import "fmt"

// marker embeds a hidden HTML comment identifying a comment as this
// agent's output for a specific commit. §6.3: "Idempotent by lookup, not
// by database — before posting, the Action checks existing comments...
// for its own hidden marker." No table, no unique key — GitHub's comment
// body is the only place this identity is recorded.
func marker(sha string) string {
	return fmt.Sprintf("<!-- ai-ci-agent:marker:sha=%s -->", sha)
}

// reviewMarker is marker's counterpart for the plain PR-review path
// (pull_request opened/synchronize). It must be a distinct string from
// marker's, not just a distinct call: a CI-failure investigation and a
// plain review can both run for the same commit SHA, and findByMarker's
// substring search would otherwise treat one as satisfying the other,
// silently skipping a post that should have happened.
func reviewMarker(sha string) string {
	return fmt.Sprintf("<!-- ai-ci-agent:review-marker:sha=%s -->", sha)
}

// passMarker is marker's counterpart for a passing CI run's short
// templated comment. Distinct from both marker and reviewMarker so none
// of the three ever shadow each other in findByMarker's substring
// lookup.
func passMarker(sha string) string {
	return fmt.Sprintf("<!-- ai-ci-agent:pass-marker:sha=%s -->", sha)
}
