package notify

import (
	"context"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
)

type pullRequestSummary struct {
	Number int `json:"number"`
}

// listOpenPRNumbers pages through every open PR, shared by
// FindPRByThreadRoot's early-exit search and ListOpenPRThreadRoots' full
// sweep so the pagination logic lives in one place.
func listOpenPRNumbers(ctx context.Context, client *ghclient.Client) ([]int, error) {
	var numbers []int
	for page := 1; page <= 5; page++ {
		var batch []pullRequestSummary
		path := client.RepoPath("/pulls?state=open&per_page=100&page=%d", page)
		if err := client.GetJSON(ctx, path, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			return numbers, nil
		}
		for _, pr := range batch {
			numbers = append(numbers, pr.Number)
		}
		if len(batch) < 100 {
			return numbers, nil
		}
	}
	return numbers, nil
}

// FindPRByThreadRoot is FindThreadRoot's reverse: given a Slack thread's
// root ts, find which open PR it belongs to. There's no direct index for
// this -- SaveThreadRoot only ever recorded the forward direction (PR ->
// ts) -- so this scans every open PR's marker comment via FindThreadRoot
// until one matches, the same "idempotent by lookup, not by database"
// idiom the rest of this package already uses, rather than introducing a
// second, separately-maintained store that could drift out of sync with
// the marker comments themselves. found is false (with a nil error) when
// no open PR's thread root matches threadTS -- e.g. the thread belongs to
// a since-closed/merged PR, which this v1 doesn't scan.
func FindPRByThreadRoot(ctx context.Context, client *ghclient.Client, threadTS string) (prNumber int, found bool, err error) {
	numbers, err := listOpenPRNumbers(ctx, client)
	if err != nil {
		return 0, false, err
	}
	for _, n := range numbers {
		ts, err := FindThreadRoot(ctx, client, n)
		if err != nil {
			return 0, false, err
		}
		if ts == threadTS {
			return n, true, nil
		}
	}
	return 0, false, nil
}

// ListOpenPRThreadRoots builds the full ts -> PR-number map across every
// open PR in one pass -- internal/slackbot's cache uses this for its
// periodic refresh sweep, so the common case (a mention on an
// already-open, already-scanned PR) is an in-memory hit at mention-time
// instead of a fresh scan. PRs with no saved thread root (e.g. opened
// while Slack notifications were disabled) are simply omitted, not an
// error.
func ListOpenPRThreadRoots(ctx context.Context, client *ghclient.Client) (map[string]int, error) {
	numbers, err := listOpenPRNumbers(ctx, client)
	if err != nil {
		return nil, err
	}
	roots := make(map[string]int, len(numbers))
	for _, n := range numbers {
		ts, err := FindThreadRoot(ctx, client, n)
		if err != nil {
			return nil, err
		}
		if ts != "" {
			roots[ts] = n
		}
	}
	return roots, nil
}
