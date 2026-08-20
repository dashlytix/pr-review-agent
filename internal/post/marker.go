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
