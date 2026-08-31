// Package githubauth abstracts how a GitHub API token is obtained, so
// internal/ghclient (and everything built on it: internal/gather,
// internal/post, the webhook path in internal/webhook) is no longer
// tightly coupled to a single static bearer token.
//
// Today, every entrypoint (cmd/agent, cmd/slackbot, cmd/webhookserver)
// uses StaticTokenAuthenticator -- a thin wrapper around the same plain
// PAT/GITHUB_TOKEN this repo has always used. AppAuthenticator
// implements the eventual production path for a real, organization-owned
// GitHub App:
//
//	GitHub App private key -> signed App JWT -> POST
//	/app/installations/{id}/access_tokens -> installation access token
//
// AppAuthenticator's code is complete and tested (see app_test.go, which
// uses a locally generated RSA key and a stub HTTP server -- no real App
// is required for it to be correct), but it isn't wired into any
// entrypoint's default configuration yet: no organization-owned App has
// been created, and swapping StaticTokenAuthenticator for AppAuthenticator
// is a configuration change (which Authenticator a caller constructs),
// not a code change to this package or to ghclient.
package githubauth

import "context"

// Authenticator resolves a GitHub API token scoped to one App
// installation. installationID is ignored by implementations with no
// installation concept (StaticTokenAuthenticator).
type Authenticator interface {
	InstallationToken(ctx context.Context, installationID int64) (string, error)
}
