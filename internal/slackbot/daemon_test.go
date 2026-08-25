package slackbot

import "testing"

func TestSeenSet_AddIfNew_DetectsDuplicates(t *testing.T) {
	s := newSeenSet(10)
	if !s.addIfNew("a") {
		t.Error("first insert of a new id should return true")
	}
	if s.addIfNew("a") {
		t.Error("re-adding the same id should return false")
	}
}

// addIfNew has a side effect (it inserts on a miss, which can itself
// trigger another eviction), so each assertion below uses a fresh set
// to avoid one check's insert perturbing the next check's expectation.

func TestSeenSet_AddIfNew_EvictedEntryCanBeReAdded(t *testing.T) {
	s := newSeenSet(2)
	s.addIfNew("a")
	s.addIfNew("b")
	s.addIfNew("c") // capacity 2 -- evicts "a", leaving {b, c}

	if !s.addIfNew("a") {
		t.Error("expected \"a\" to have been evicted and thus re-addable as new")
	}
}

func TestSeenSet_AddIfNew_NonEvictedEntryStaysPresent(t *testing.T) {
	s := newSeenSet(2)
	s.addIfNew("a")
	s.addIfNew("b")
	s.addIfNew("c") // capacity 2 -- evicts "a", leaving {b, c}

	if s.addIfNew("c") {
		t.Error("expected \"c\" to still be present (not evicted)")
	}
	if s.addIfNew("b") {
		t.Error("expected \"b\" to still be present (not evicted)")
	}
}
