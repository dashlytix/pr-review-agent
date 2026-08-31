package webhook

import (
	"context"
	"sync"
	"testing"
)

func TestInMemoryIdempotencyStore_FirstDeliveryIsProcessed(t *testing.T) {
	s := NewInMemoryIdempotencyStore()
	seen, err := s.Has(context.Background(), "delivery-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seen {
		t.Error("a fresh store must not report an unmarked delivery as seen")
	}
}

func TestInMemoryIdempotencyStore_DuplicateDeliveryIsIgnored(t *testing.T) {
	s := NewInMemoryIdempotencyStore()
	ctx := context.Background()

	if err := s.Mark(ctx, "delivery-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	seen, err := s.Has(ctx, "delivery-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !seen {
		t.Error("expected a marked delivery to report as seen")
	}

	// Marking again must stay a no-op, not an error.
	if err := s.Mark(ctx, "delivery-1"); err != nil {
		t.Errorf("re-marking an already-marked delivery should not error, got: %v", err)
	}
}

func TestInMemoryIdempotencyStore_UnrelatedDeliveryIDsDoNotCollide(t *testing.T) {
	s := NewInMemoryIdempotencyStore()
	ctx := context.Background()
	s.Mark(ctx, "delivery-1")

	seen, _ := s.Has(ctx, "delivery-2")
	if seen {
		t.Error("marking one delivery ID must not affect another")
	}
}

func TestInMemoryIdempotencyStore_CheckAndMarkFirstCallReportsUnseen(t *testing.T) {
	s := NewInMemoryIdempotencyStore()
	alreadySeen, err := s.CheckAndMark(context.Background(), "delivery-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alreadySeen {
		t.Error("the first CheckAndMark for a fresh ID must report alreadySeen=false")
	}
}

func TestInMemoryIdempotencyStore_CheckAndMarkSecondCallReportsSeen(t *testing.T) {
	s := NewInMemoryIdempotencyStore()
	ctx := context.Background()
	s.CheckAndMark(ctx, "delivery-1")

	alreadySeen, err := s.CheckAndMark(ctx, "delivery-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !alreadySeen {
		t.Error("a repeated CheckAndMark for the same ID must report alreadySeen=true")
	}
}

// TestInMemoryIdempotencyStore_ConcurrentDuplicateDeliveriesAreHandledSafely
// fires many goroutines at the same delivery ID simultaneously via
// CheckAndMark -- the store's mutex must serialize them so exactly one
// goroutine observes alreadySeen=false (the "winner" that should proceed
// to run the review pipeline) and every other goroutine observes true.
func TestInMemoryIdempotencyStore_ConcurrentDuplicateDeliveriesAreHandledSafely(t *testing.T) {
	s := NewInMemoryIdempotencyStore()
	ctx := context.Background()
	const attempts = 200

	var wg sync.WaitGroup
	var mu sync.Mutex
	unseenCount := 0

	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			alreadySeen, err := s.CheckAndMark(ctx, "delivery-race")
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if !alreadySeen {
				mu.Lock()
				unseenCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if unseenCount != 1 {
		t.Errorf("exactly one concurrent CheckAndMark call should have won (alreadySeen=false), got %d", unseenCount)
	}
}

// A separate delivery ID racing concurrently with the one above must be
// unaffected -- concurrency safety must be per-key correct, not just
// "no data race" in the race-detector sense.
func TestInMemoryIdempotencyStore_ConcurrentDistinctDeliveriesAllWin(t *testing.T) {
	s := NewInMemoryIdempotencyStore()
	ctx := context.Background()
	const n = 100

	var wg sync.WaitGroup
	results := make([]bool, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			alreadySeen, err := s.CheckAndMark(ctx, deliveryIDForTest(i))
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			results[i] = alreadySeen
		}()
	}
	wg.Wait()

	for i, alreadySeen := range results {
		if alreadySeen {
			t.Errorf("delivery %d: alreadySeen = true, want false (each ID is distinct)", i)
		}
	}
}

func deliveryIDForTest(i int) string {
	return "delivery-distinct-" + string(rune('a'+i%26)) + string(rune('0'+i/26))
}
