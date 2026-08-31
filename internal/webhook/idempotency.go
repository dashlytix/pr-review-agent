package webhook

import (
	"context"
	"sync"
)

// IdempotencyStore records which webhook delivery IDs (the
// X-GitHub-Delivery header) have already been processed, so a
// redelivered event -- GitHub retries a delivery that times out or
// answers non-2xx -- never triggers the review pipeline a second time.
//
// This is deliberately an interface, not a concrete type, so the
// production deployment can later swap InMemoryIdempotencyStore for a
// durable, shared store (e.g. Postgres) that survives a process restart
// and is consistent across multiple webhook-server replicas, without
// Handler or any of its callers changing at all.
type IdempotencyStore interface {
	// Has reports whether deliveryID has already been marked processed.
	Has(ctx context.Context, deliveryID string) (bool, error)
	// Mark records deliveryID as processed. Marking an already-marked ID
	// is a no-op, not an error.
	Mark(ctx context.Context, deliveryID string) error
	// CheckAndMark atomically checks and marks deliveryID in one
	// operation: it reports whether deliveryID was already marked
	// *before* this call, and if not, marks it as part of the same
	// operation. Handler uses this (not a separate Has-then-Mark pair)
	// specifically so two concurrent deliveries of the same ID can't
	// both observe "not yet seen" before either one marks it -- Has and
	// Mark remain on the interface for callers that only need one half
	// (tests, inspection, a manual reconciliation tool).
	CheckAndMark(ctx context.Context, deliveryID string) (alreadySeen bool, err error)
}

// InMemoryIdempotencyStore is a process-lifetime, in-memory
// IdempotencyStore.
//
// TEMPORARY: this is explicitly a development/testing implementation,
// not the final production design. It does not survive a process
// restart, and it is not shared across multiple webhook-server replicas
// behind a load balancer -- either case lets a redelivery slip through
// and reprocess. It also grows unbounded for the life of the process
// (no eviction), which is fine for development and short-lived test
// runs but not for a long-running production deployment. Production
// needs a durable, shared implementation (e.g. Postgres, keyed on
// delivery ID with a unique constraint) satisfying this same
// IdempotencyStore interface -- swapped in via whatever constructs a
// Handler, with no change to Handler itself.
type InMemoryIdempotencyStore struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

// NewInMemoryIdempotencyStore returns an empty store, ready to use.
func NewInMemoryIdempotencyStore() *InMemoryIdempotencyStore {
	return &InMemoryIdempotencyStore{seen: make(map[string]struct{})}
}

func (s *InMemoryIdempotencyStore) Has(ctx context.Context, deliveryID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[deliveryID]
	return ok, nil
}

func (s *InMemoryIdempotencyStore) Mark(ctx context.Context, deliveryID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen[deliveryID] = struct{}{}
	return nil
}

func (s *InMemoryIdempotencyStore) CheckAndMark(ctx context.Context, deliveryID string) (alreadySeen bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, alreadySeen = s.seen[deliveryID]
	if !alreadySeen {
		s.seen[deliveryID] = struct{}{}
	}
	return alreadySeen, nil
}
