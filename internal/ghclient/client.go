// Package ghclient is a minimal GitHub REST API client. It exists so
// internal/gather and internal/post share one auth/retry/backoff
// implementation instead of each rolling their own HTTP plumbing.
package ghclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.github.com"

// Client is a thin, retrying wrapper around net/http scoped to the GitHub
// REST API. It carries no state beyond what's needed to authenticate and
// address a single repository, matching the stateless design in ADR-001 §3.
type Client struct {
	HTTP    *http.Client
	BaseURL string // overridable so tests can point this at an httptest.Server
	Token   string
	Owner   string
	Repo    string
	// MaxAttempts bounds retry-with-backoff on rate limiting (§7:
	// "retries with backoff within the step's time budget, then falls
	// back to a minimal comment").
	MaxAttempts int
	// RetryBaseDelay is the first backoff wait (doubled each subsequent
	// attempt). Exposed so tests can shrink it instead of waiting out a
	// real multi-second backoff.
	RetryBaseDelay time.Duration
}

// New builds a Client for one repository.
//
// The API host honours GITHUB_API_URL, which the Actions runner sets on
// every job. Reading it means the same binary works on GitHub Enterprise
// (where the host is not api.github.com) instead of hardcoding the public
// API, and lets an end-to-end test point the whole agent at a stub
// server without reaching the network.
func New(token, owner, repo string) *Client {
	base := defaultBaseURL
	if v := strings.TrimRight(strings.TrimSpace(os.Getenv("GITHUB_API_URL")), "/"); v != "" {
		base = v
	}
	return &Client{
		HTTP:           &http.Client{Timeout: 30 * time.Second},
		BaseURL:        base,
		Token:          token,
		Owner:          owner,
		Repo:           repo,
		MaxAttempts:    4,
		RetryBaseDelay: 2 * time.Second,
	}
}

// RateLimitedError is returned when every retry attempt was exhausted while
// GitHub kept returning a rate-limit response.
type RateLimitedError struct {
	Path string
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("github: rate limited after retries: %s", e.Path)
}

// do executes a request with auth headers and exponential-backoff retry on
// secondary rate limiting (403/429 with a Retry-After or reset header).
func (c *Client) do(ctx context.Context, method, path string, accept string, body io.Reader) (*http.Response, error) {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, err
		}
	}

	var lastErr error
	backoff := c.RetryBaseDelay
	for attempt := 1; attempt <= c.MaxAttempts; attempt++ {
		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reqBody)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if accept != "" {
			req.Header.Set("Accept", accept)
		} else {
			req.Header.Set("Accept", "application/vnd.github+json")
		}
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			continue
		}

		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			if isRateLimited(resp) && attempt < c.MaxAttempts {
				wait := retryAfter(resp, backoff)
				resp.Body.Close()
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(wait):
				}
				backoff *= 2
				continue
			}
			if isRateLimited(resp) {
				resp.Body.Close()
				return nil, &RateLimitedError{Path: path}
			}
		}

		return resp, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &RateLimitedError{Path: path}
}

func isRateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	if resp.StatusCode == http.StatusForbidden {
		if resp.Header.Get("Retry-After") != "" {
			return true
		}
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return true
		}
	}
	return false
}

func retryAfter(resp *http.Response, fallback time.Duration) time.Duration {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	if reset := resp.Header.Get("X-RateLimit-Reset"); reset != "" {
		if ts, err := strconv.ParseInt(reset, 10, 64); err == nil {
			d := time.Until(time.Unix(ts, 0))
			if d > 0 && d < 5*time.Minute {
				return d
			}
		}
	}
	return fallback
}

// GetJSON issues a GET request and decodes a JSON response body into out.
func (c *Client) GetJSON(ctx context.Context, path string, out interface{}) error {
	resp, err := c.do(ctx, http.MethodGet, path, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return statusError(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// GetRaw issues a GET request with a custom Accept header and returns the
// raw response body. Used for the diff media type and log downloads.
func (c *Client) GetRaw(ctx context.Context, path, accept string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, path, accept, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, statusError(resp)
	}
	return io.ReadAll(resp.Body)
}

// PostJSON issues a POST request with a JSON body and decodes the response
// into out (if non-nil).
func (c *Client) PostJSON(ctx context.Context, path string, in interface{}, out interface{}) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	resp, err := c.do(ctx, http.MethodPost, path, "", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return statusError(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("github: %s %s: %d: %s", resp.Request.Method, resp.Request.URL.Path, resp.StatusCode, string(body))
}

// RepoPath builds a /repos/{owner}/{repo}/... path.
func (c *Client) RepoPath(format string, args ...interface{}) string {
	prefix := fmt.Sprintf("/repos/%s/%s", c.Owner, c.Repo)
	return prefix + fmt.Sprintf(format, args...)
}
