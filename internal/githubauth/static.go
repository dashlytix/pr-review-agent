package githubauth

import "context"

// StaticTokenAuthenticator wraps today's existing GitHub authentication
// (a plain PAT / GITHUB_TOKEN / Actions github-token input) behind the
// Authenticator interface, ignoring installationID entirely -- there is
// no installation concept for a static token. This is what every
// existing entrypoint uses today, and what a new one (cmd/webhookserver)
// uses until an organization admin creates and installs a real GitHub
// App.
type StaticTokenAuthenticator struct {
	Token string
}

// InstallationToken always returns the configured static token,
// regardless of installationID.
func (a StaticTokenAuthenticator) InstallationToken(ctx context.Context, installationID int64) (string, error) {
	return a.Token, nil
}
