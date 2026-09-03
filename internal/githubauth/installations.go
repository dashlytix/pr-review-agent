package githubauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// installationsPerPage is GitHub's maximum page size for both list
// endpoints below, so a dashboard with a handful of installations and
// repos never needs more than one page in practice.
const installationsPerPage = 100

// Installation is the subset of GitHub's installation object needed to
// show which accounts have installed this App.
// https://docs.github.com/en/rest/apps/apps#list-installations-for-the-authenticated-app
type Installation struct {
	ID                  int64               `json:"id"`
	Account             InstallationAccount `json:"account"`
	RepositorySelection string              `json:"repository_selection"` // "all" or "selected"
	HTMLURL             string              `json:"html_url"`
}

// InstallationAccount is the org or user an installation belongs to.
type InstallationAccount struct {
	Login string `json:"login"`
	Type  string `json:"type"` // "Organization" or "User"
}

// ListInstallations returns every installation of this App, across all
// pages, authenticated as the App itself (a fresh App JWT -- the same
// credential InstallationToken mints one for) via
// GET /app/installations. Unlike InstallationToken, no installation ID
// is required -- this is how a caller (the dashboard) discovers which
// installation IDs exist in the first place.
func (a *AppAuthenticator) ListInstallations(ctx context.Context) ([]Installation, error) {
	jwt, err := a.signJWT(time.Now())
	if err != nil {
		return nil, fmt.Errorf("githubauth: sign app jwt: %w", err)
	}

	base := a.baseURL()
	var all []Installation
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/app/installations?per_page=%d&page=%d", base, installationsPerPage, page)
		var batch []Installation
		if err := a.doAppRequest(ctx, url, jwt, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
		if len(batch) < installationsPerPage {
			return all, nil
		}
	}
}

// Repository is the subset of GitHub's repository object needed to list
// which repos an installation covers.
type Repository struct {
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
	HTMLURL  string `json:"html_url"`
}

// listInstallationRepositoriesResponse is the wrapped shape
// GET /installation/repositories returns, unlike /app/installations'
// bare array.
type listInstallationRepositoriesResponse struct {
	TotalCount   int          `json:"total_count"`
	Repositories []Repository `json:"repositories"`
}

// ListInstallationRepositories returns every repository installationID
// currently covers, across all pages, via GET /installation/repositories.
// It mints its own fresh installation access token through
// InstallationToken rather than accepting one as a parameter, so a
// caller never holds a token across requests -- callers (the dashboard)
// re-derive this on every page load per this repo's stateless
// philosophy (ADR-001 §3), so there is never a stale token to
// accidentally reuse.
func (a *AppAuthenticator) ListInstallationRepositories(ctx context.Context, installationID int64) ([]Repository, error) {
	token, err := a.InstallationToken(ctx, installationID)
	if err != nil {
		return nil, err
	}

	base := a.baseURL()
	var all []Repository
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s/installation/repositories?per_page=%d&page=%d", base, installationsPerPage, page)
		var out listInstallationRepositoriesResponse
		if err := a.doAppRequest(ctx, url, token, &out); err != nil {
			return nil, err
		}
		all = append(all, out.Repositories...)
		if len(out.Repositories) < installationsPerPage {
			return all, nil
		}
	}
}

// doAppRequest issues an authenticated GET against url, decoding a 2xx
// JSON response into out and turning any non-2xx response into an error
// carrying the API's own message -- the same shape InstallationToken
// already implements inline, factored out here so the two list methods
// above don't duplicate it. InstallationToken itself is left untouched.
//
// Known limitation, deliberately deferred: unlike internal/ghclient.Client,
// this does not retry on rate limiting (403/429) -- a rate-limited call
// surfaces as a plain error rather than retrying with backoff. Accepted
// for now since App-authenticated calls get a generous 5,000 req/hour
// limit and this is a low-traffic internal admin tool; worth adding
// ghclient-style retry here if that ever stops being true.
func (a *AppAuthenticator) doAppRequest(ctx context.Context, url, bearerToken string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	httpClient := a.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("githubauth: request %s: %w", url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		var parsed githubAPIError
		if json.Unmarshal(raw, &parsed) == nil && parsed.Message != "" {
			msg = parsed.Message
		}
		return fmt.Errorf("githubauth: request %s failed (%d): %s", url, resp.StatusCode, msg)
	}

	return json.Unmarshal(raw, out)
}

// baseURL returns a.BaseURL, or defaultAppBaseURL if unset -- the same
// fallback InstallationToken applies inline.
func (a *AppAuthenticator) baseURL() string {
	if a.BaseURL != "" {
		return strings.TrimRight(a.BaseURL, "/")
	}
	return defaultAppBaseURL
}
