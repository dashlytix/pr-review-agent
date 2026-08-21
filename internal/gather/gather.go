// Package gather makes every GitHub API call needed to reconstruct the
// context of one CI failure: the log tail, failed test output, the PR
// diff, and touched files. Nothing here is cached — per ADR-001 §3, every
// invocation re-derives its context from scratch.
package gather

import (
	"context"
	"fmt"
	"strings"

	"github.com/dimension/ai-ci-agent/internal/assess"
	"github.com/dimension/ai-ci-agent/internal/ghclient"
)

// logTailLines bounds how much of the failed job's log is kept, matching
// the "log tail" language in §2.1/§3 rather than shipping entire logs
// into the prompt.
const logTailLines = 400

// Result bundles the assembled AssessmentRequest with the metadata
// cmd/agent needs to post the comment and to detect a stale PR head.
type Result struct {
	Request    assess.AssessmentRequest
	PRNumber   int
	HeadSHA    string
	RunHTMLURL string
}

type workflowRun struct {
	ID      int64  `json:"id"`
	HeadSHA string `json:"head_sha"`
	HTMLURL string `json:"html_url"`
}

type jobsResponse struct {
	Jobs []job `json:"jobs"`
}

type job struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

type pullRef struct {
	Number int `json:"number"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

type prFile struct {
	Filename string `json:"filename"`
	Patch    string `json:"patch"`
}

// Gather fetches everything needed to assess the given workflow run. If
// prNumber is 0 (the triggering workflow_run event carried no associated
// pull request), it is resolved by looking up PRs associated with the
// run's head commit.
//
// On a partial failure (e.g. GitHub rate limiting hit while fetching the
// diff), Gather still returns the best-effort Result built so far
// alongside the error, rather than nil — once PRNumber and HeadSHA are
// known, the caller has enough to post a fallback/minimal comment per §7
// instead of failing the workflow outright. A nil Result with a nil error
// means there was nothing to investigate (no PR associated with this
// run); a nil Result with a non-nil error means even the workflow run
// itself couldn't be read, which is unrecoverable.
func Gather(ctx context.Context, client *ghclient.Client, runID int64, prNumber int) (*Result, error) {
	var run workflowRun
	if err := client.GetJSON(ctx, client.RepoPath("/actions/runs/%d", runID), &run); err != nil {
		return nil, fmt.Errorf("gather: fetch workflow run: %w", err)
	}

	if prNumber == 0 {
		resolved, err := resolvePRNumber(ctx, client, run.HeadSHA)
		if err != nil {
			return nil, nil // nothing to comment on — not a failure worth surfacing
		}
		prNumber = resolved
	}

	result := &Result{
		PRNumber:   prNumber,
		HeadSHA:    run.HeadSHA,
		RunHTMLURL: run.HTMLURL,
	}

	logTail, err := fetchFailedJobLogs(ctx, client, runID)
	if err != nil {
		return result, fmt.Errorf("gather: fetch job logs: %w", err)
	}
	result.Request.LogTail = logTail
	result.Request.FailedTests = ExtractFailures(logTail)

	diff, err := fetchDiff(ctx, client, prNumber)
	if err != nil {
		return result, fmt.Errorf("gather: fetch diff: %w", err)
	}
	result.Request.Diff = diff

	files, err := fetchFiles(ctx, client, prNumber)
	if err != nil {
		return result, fmt.Errorf("gather: fetch files: %w", err)
	}
	result.Request.Files = files

	return result, nil
}

// GatherForReview fetches just the PR diff and touched files for the
// plain PR-review path (pull_request opened/synchronize) — no CI run is
// involved here, so there's no log tail to fetch.
func GatherForReview(ctx context.Context, client *ghclient.Client, prNumber int) (assess.AssessmentRequest, error) {
	var req assess.AssessmentRequest

	diff, err := fetchDiff(ctx, client, prNumber)
	if err != nil {
		return req, fmt.Errorf("gather: fetch diff: %w", err)
	}
	req.Diff = diff

	files, err := fetchFiles(ctx, client, prNumber)
	if err != nil {
		return req, fmt.Errorf("gather: fetch files: %w", err)
	}
	req.Files = files

	return req, nil
}

// resolvePRNumber falls back to GitHub's "list pull requests associated
// with a commit" endpoint when the workflow_run event payload didn't
// carry pull_requests (this happens for some fork-originated runs).
func resolvePRNumber(ctx context.Context, client *ghclient.Client, sha string) (int, error) {
	var prs []pullRef
	if err := client.GetJSON(ctx, client.RepoPath("/commits/%s/pulls", sha), &prs); err != nil {
		return 0, err
	}
	if len(prs) == 0 {
		return 0, fmt.Errorf("no pull request associated with commit %s", sha)
	}
	return prs[0].Number, nil
}

func fetchFailedJobLogs(ctx context.Context, client *ghclient.Client, runID int64) (string, error) {
	var jobs jobsResponse
	if err := client.GetJSON(ctx, client.RepoPath("/actions/runs/%d/jobs?per_page=100", runID), &jobs); err != nil {
		return "", err
	}

	var combined strings.Builder
	for _, j := range jobs.Jobs {
		if j.Conclusion != "failure" {
			continue
		}
		raw, err := client.GetRaw(ctx, client.RepoPath("/actions/jobs/%d/logs", j.ID), "")
		if err != nil {
			// A single job's log being unavailable shouldn't sink the
			// whole gather step — other failed jobs may still be useful.
			continue
		}
		combined.WriteString(fmt.Sprintf("=== job: %s ===\n", j.Name))
		combined.Write(raw)
		combined.WriteString("\n")
	}

	return tailLines(combined.String(), logTailLines), nil
}

func fetchDiff(ctx context.Context, client *ghclient.Client, prNumber int) (string, error) {
	raw, err := client.GetRaw(ctx, client.RepoPath("/pulls/%d", prNumber), "application/vnd.github.v3.diff")
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func fetchFiles(ctx context.Context, client *ghclient.Client, prNumber int) (map[string]string, error) {
	files := make(map[string]string)
	for page := 1; page <= 5; page++ {
		var batch []prFile
		path := client.RepoPath("/pulls/%d/files?per_page=100&page=%d", prNumber, page)
		if err := client.GetJSON(ctx, path, &batch); err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, f := range batch {
			files[f.Filename] = f.Patch
		}
		if len(batch) < 100 {
			break
		}
	}
	return files, nil
}

func tailLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
