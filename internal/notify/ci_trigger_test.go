package notify

import "testing"

// TestTHROWAWAY_TriggerCIFailure is a deliberate, temporary failure to
// exercise the CI-check-failed -> Slack thread-reply path end to end on
// PR #25. Removed once verified.
func TestTHROWAWAY_TriggerCIFailure(t *testing.T) {
	t.Fatal("intentional failure to trigger a real CI-failed Slack notification")
}
