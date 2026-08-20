package gather

import (
	"context"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
)

// RunRef identifies a workflow run for the reconciliation sweep.
type RunRef struct {
	ID int64
}

type runsResponse struct {
	WorkflowRuns []struct {
		ID         int64  `json:"id"`
		Conclusion string `json:"conclusion"`
	} `json:"workflow_runs"`
}

// RecentFailedRuns lists the most recent failed workflow runs, for the
// scheduled reconciliation poll (§7: "Trigger webhook dropped or the
// notify step itself fails — a scheduled reconciliation run... checks
// recent failed workflow runs for a missing marker comment and catches
// up any that were missed"). limit bounds the page size; the caller is
// expected to run this every ~30 minutes, so a small recent window is
// enough to catch anything a dropped webhook missed.
func RecentFailedRuns(ctx context.Context, client *ghclient.Client, limit int) ([]RunRef, error) {
	var resp runsResponse
	path := client.RepoPath("/actions/runs?status=failure&per_page=%d", limit)
	if err := client.GetJSON(ctx, path, &resp); err != nil {
		return nil, err
	}

	refs := make([]RunRef, 0, len(resp.WorkflowRuns))
	for _, r := range resp.WorkflowRuns {
		refs = append(refs, RunRef{ID: r.ID})
	}
	return refs, nil
}
