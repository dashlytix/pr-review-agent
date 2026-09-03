package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// defaultGitHubAPIURL and defaultGitHubOAuthURL are overridable (see
// Handler.apiBaseURL/oauthBaseURL) only by tests, which point both at
// one httptest.Server -- production always uses these.
const (
	defaultGitHubAPIURL   = "https://api.github.com"
	defaultGitHubOAuthURL = "https://github.com"
)

// githubAPIError mirrors internal/githubauth's error-message shape
// (GitHub's API error bodies are consistently {"message": "..."}
// regardless of which endpoint returns them) -- duplicated rather than
// imported since this file authenticates as the signed-in user, never
// the App, and the two packages otherwise share nothing.
type githubAPIError struct {
	Message string `json:"message"`
}

// exchangeCode trades a GitHub OAuth "code" for a user access token via
// POST {oauthBaseURL}/login/oauth/access_token. This calls GitHub as
// the App's own OAuth client (clientID/clientSecret), not as the user
// -- the user has no credential yet at this point, that's the whole
// purpose of this exchange.
func exchangeCode(ctx context.Context, httpClient *http.Client, oauthBaseURL, clientID, clientSecret, code, redirectURI string) (string, error) {
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthBaseURL+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("dashboard: exchange oauth code: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("dashboard: oauth code exchange failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("dashboard: decode oauth token response: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("dashboard: oauth code exchange rejected: %s (%s)", out.Error, out.ErrorDesc)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("dashboard: oauth token response carried no access_token")
	}
	return out.AccessToken, nil
}

// githubLogin calls GET /user with the user's own OAuth token, returning
// their login for display only -- isOrgAdmin is what actually gates
// access; this exists so the dashboard can greet a signed-in admin by
// name, nothing more.
func githubLogin(ctx context.Context, httpClient *http.Client, apiBaseURL, userToken string) (string, error) {
	var out struct {
		Login string `json:"login"`
	}
	if err := doUserRequest(ctx, httpClient, apiBaseURL+"/user", userToken, &out); err != nil {
		return "", err
	}
	return out.Login, nil
}

// orgMembership is the subset of GitHub's org-membership object needed
// to decide dashboard access.
// https://docs.github.com/en/rest/orgs/members#get-organization-membership-for-a-user
type orgMembership struct {
	Role  string `json:"role"`  // "admin" or "member"
	State string `json:"state"` // "active" or "pending"
}

// isOrgAdmin calls GET /user/memberships/orgs/{org} authenticated as
// the signed-in user (their own OAuth token, never the App's), and
// reports whether they are an active admin of org. A 404 (not a member
// of org at all) is treated as "not an admin", not an error -- GitHub
// returns 404 rather than a membership object with role="none" for a
// non-member. Any other non-2xx is returned as an error, so a transient
// GitHub outage is distinguishable from a confirmed non-admin --
// requireAdmin (handler.go) fails closed on the former, denies on the
// latter.
func isOrgAdmin(ctx context.Context, httpClient *http.Client, apiBaseURL, userToken, org string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBaseURL+"/user/memberships/orgs/"+url.PathEscape(org), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+userToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("dashboard: check org membership: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		var parsed githubAPIError
		if json.Unmarshal(raw, &parsed) == nil && parsed.Message != "" {
			msg = parsed.Message
		}
		return false, fmt.Errorf("dashboard: check org membership failed (%d): %s", resp.StatusCode, msg)
	}

	var membership orgMembership
	if err := json.Unmarshal(raw, &membership); err != nil {
		return false, fmt.Errorf("dashboard: decode org membership: %w", err)
	}
	return membership.Role == "admin" && membership.State == "active", nil
}

// doUserRequest issues an authenticated GET against url as the given
// user token, decoding a 2xx JSON response into out. Shared by
// githubLogin (isOrgAdmin has its own copy, since it also needs to
// special-case 404 before reading the body as an error).
func doUserRequest(ctx context.Context, httpClient *http.Client, url, userToken string, out interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+userToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("dashboard: request %s: %w", url, err)
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
		return fmt.Errorf("dashboard: request %s failed (%d): %s", url, resp.StatusCode, msg)
	}
	return json.Unmarshal(raw, out)
}
