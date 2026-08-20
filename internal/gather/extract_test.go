package gather

import (
	"strings"
	"testing"
)

func TestExtractFailures_Go(t *testing.T) {
	log := "ok  \tsome/other/pkg\t0.01s\n--- FAIL: TestGetOwner (0.00s)\npanic: runtime error: invalid memory address or nil pointer dereference\nvehicles/garage.go:42:5: nil check missing\nFAIL\tvehicles\t0.014s\n"

	got := ExtractFailures(log)

	mustContain(t, got, "--- FAIL: TestGetOwner (0.00s)")
	mustContain(t, got, "panic: runtime error")
	mustNotContain(t, got, "ok  \tsome/other/pkg\t0.01s")
}

func TestExtractFailures_Rust(t *testing.T) {
	log := "running 1 test\nthread 'inventory::tests::last_part' panicked at src/inventory.rs:27:20:\nindex out of bounds: the len is 0 but the index is 0\ntest result: FAILED. 0 passed; 1 failed; 0 ignored\n"

	got := ExtractFailures(log)

	mustContain(t, got, "thread 'inventory::tests::last_part' panicked at src/inventory.rs:27:20:")
	mustContain(t, got, "test result: FAILED")
}

func TestExtractFailures_TypeScript(t *testing.T) {
	log := "$ tsc -p .\nsrc/quotes/pricing.ts(15,32): error TS2345: Argument of type 'string' is not assignable to parameter of type 'number'.\nFound 1 error.\n"

	got := ExtractFailures(log)

	mustContain(t, got, "error TS2345")
}

func TestExtractFailures_SQL(t *testing.T) {
	log := "Applying migration 0027_add_work_order_notes.sql\nERROR:  relation \"work_orders\" does not exist\nmigration failed: 0027_add_work_order_notes.sql\n"

	got := ExtractFailures(log)

	mustContain(t, got, `relation "work_orders" does not exist`)
	mustContain(t, got, "migration failed")
}

func TestExtractFailures_NoMatchesReturnsEmpty(t *testing.T) {
	log := "Building...\nRunning tests...\nAll 42 tests passed.\nDone.\n"

	got := ExtractFailures(log)

	if got != "" {
		t.Errorf("expected no extracted lines from a clean log, got %q", got)
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected output to contain %q, got:\n%s", needle, haystack)
	}
}

func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("expected output to NOT contain %q, got:\n%s", needle, haystack)
	}
}
