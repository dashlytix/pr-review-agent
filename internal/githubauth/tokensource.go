package githubauth

import (
	"context"
	"sync"
	"time"
)

// installationTokenTTLGuess is a conservative estimate of how long a
// freshly minted installation token stays valid, used to decide when
// InstallationTokenSource should refresh. GitHub's real tokens last
// about an hour; refreshing somewhat earlier than that costs one extra
// exchange in the rare case a request lands right at the boundary, which
// is cheap next to the alternative of handing out an expired token.
const installationTokenTTLGuess = 45 * time.Minute

// InstallationTokenSource adapts an Authenticator, scoped to one fixed
// installation, into a ghclient.TokenSource: ghclient.Client is already
// scoped to one repository/one credential per instance (see
// ghclient.New), so there is exactly one installation ID to close over
// per Client. Tokens are cached until shortly before their assumed
// expiry so a busy client doesn't mint a fresh installation token on
// every single API call -- InstallationToken (the underlying network
// call) is comparatively expensive: it signs a JWT and makes an extra
// GitHub API round-trip beyond whatever call the caller actually wanted.
type InstallationTokenSource struct {
	Auth           Authenticator
	InstallationID int64

	mu      sync.Mutex
	token   string
	expires time.Time
}

// Token implements ghclient.TokenSource.
func (s *InstallationTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && time.Now().Before(s.expires) {
		return s.token, nil
	}

	tok, err := s.Auth.InstallationToken(ctx, s.InstallationID)
	if err != nil {
		return "", err
	}
	s.token = tok
	s.expires = time.Now().Add(installationTokenTTLGuess)
	return s.token, nil
}
