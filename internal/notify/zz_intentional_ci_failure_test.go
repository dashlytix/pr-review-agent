package notify

import "testing"

// TestZZIntentionalCIFailure is a deliberate, temporary failure used to
// exercise the CI-failed Slack notification and the AI CI Agent's
// diagnosis comment end-to-end. This file and its throwaway PR are not
// meant to be merged -- delete before merging.
func TestZZIntentionalCIFailure(t *testing.T) {
	t.Fatal("intentional failure for end-to-end Slack notification testing -- safe to ignore, this PR is not meant to be merged")
}
