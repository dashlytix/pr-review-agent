package slackbot

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
	"github.com/dimension/ai-ci-agent/internal/notify"
)

// prCache is a process-lifetime, in-memory-only cache mapping a Slack
// thread-root ts to the open PR it belongs to. The source of truth stays
// the GitHub marker comments themselves (internal/notify's
// FindThreadRoot/FindPRByThreadRoot/ListOpenPRThreadRoots) -- this cache
// is purely a performance optimization so a mention doesn't pay a live
// GitHub scan every time, and it's safe to lose (e.g. on restart) or
// rebuild at any point without corrupting anything, matching this
// repo's "idempotent by lookup, not by database" design (see
// internal/post/marker.go).
type prCache struct {
	client *ghclient.Client

	mu   sync.RWMutex
	byTS map[string]int
}

func newPRCache(client *ghclient.Client) *prCache {
	return &prCache{client: client, byTS: make(map[string]int)}
}

// lookup checks the in-memory cache first, falling back to a live scan
// (via notify.FindPRByThreadRoot) on a miss -- so a PR opened in between
// refresh sweeps still resolves correctly, just at the cost of one live
// scan instead of an O(1) hit. found is false, err nil for a genuine
// "no open PR has this thread root" (see FindPRByThreadRoot's own doc).
func (c *prCache) lookup(ctx context.Context, threadTS string) (prNumber int, found bool, err error) {
	c.mu.RLock()
	prNumber, found = c.byTS[threadTS]
	c.mu.RUnlock()
	if found {
		return prNumber, true, nil
	}

	prNumber, found, err = notify.FindPRByThreadRoot(ctx, c.client, threadTS)
	if err != nil || !found {
		return 0, false, err
	}
	c.mu.Lock()
	c.byTS[threadTS] = prNumber
	c.mu.Unlock()
	return prNumber, true, nil
}

// refresh does a full sweep of open PRs and repopulates the cache in one
// pass, so the common case (a mention on an already-open,
// already-scanned PR) is an in-memory hit at mention-time with zero
// GitHub API calls. A failed sweep is logged, not fatal -- it just
// leaves the existing (possibly stale, possibly empty) cache in place;
// lookup's own lazy fallback still covers a miss either way.
func (c *prCache) refresh(ctx context.Context) {
	fresh, err := notify.ListOpenPRThreadRoots(ctx, c.client)
	if err != nil {
		log.Printf("slackbot: cache refresh failed: %v", err)
		return
	}
	c.mu.Lock()
	c.byTS = fresh
	c.mu.Unlock()
}

// runRefresh sweeps immediately, then on interval, until ctx is done.
// Meant to run in its own goroutine for the daemon's lifetime.
func (c *prCache) runRefresh(ctx context.Context, interval time.Duration) {
	c.refresh(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.refresh(ctx)
		}
	}
}
