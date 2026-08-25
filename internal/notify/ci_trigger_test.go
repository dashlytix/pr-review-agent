package notify

import "testing"

// TestTHROWAWAY_TriggerCIFailure is a deliberate, temporary failure used
// to verify the CI-check-failed -> Slack thread-reply path end to end
// now that the threaded notification code has been merged to main.
// Removed once verified.
func TestTHROWAWAY_TriggerCIFailure(t *testing.T) {
	t.Fatal("intentional failure to verify the merged CI-failed Slack thread reply")
}
