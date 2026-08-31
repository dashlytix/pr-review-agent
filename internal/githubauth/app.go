package githubauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultAppBaseURL = "https://api.github.com"

// jwtTTL is how long a generated App JWT is valid for. GitHub caps this
// at 10 minutes; kept comfortably under that, and issued with a small
// clock-skew allowance (see signJWT) matching GitHub's own documented
// guidance for App JWTs.
const jwtTTL = 9 * time.Minute

// clockSkewAllowance backdates "iat" slightly so a JWT is still accepted
// if this process's clock is a little ahead of GitHub's.
const clockSkewAllowance = 60 * time.Second

// AppAuthenticator implements Authenticator via GitHub App
// authentication: it signs a short-lived App JWT with the App's RSA
// private key, then exchanges that JWT for an installation access token
// via GitHub's REST API. See the package doc comment for why this exists
// today with nothing yet configured to use it by default.
type AppAuthenticator struct {
	// AppID is the GitHub App's numeric ID (the JWT's "iss" claim), as a
	// string since it's most often read straight from an env var.
	AppID string
	// PrivateKey is the App's RSA private key (downloaded once from the
	// App's settings page as a .pem file).
	PrivateKey *rsa.PrivateKey
	// BaseURL defaults to https://api.github.com; overridable for
	// GitHub Enterprise or for pointing a test at an httptest.Server.
	BaseURL string
	// HTTP defaults to http.DefaultClient if nil.
	HTTP *http.Client
}

// NewAppAuthenticator parses a PEM-encoded RSA private key (as
// downloaded from a GitHub App's settings page, PKCS#1 or PKCS#8) and
// returns a ready-to-use AppAuthenticator.
func NewAppAuthenticator(appID string, privateKeyPEM []byte) (*AppAuthenticator, error) {
	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("githubauth: parse private key: %w", err)
	}
	return &AppAuthenticator{AppID: appID, PrivateKey: key}, nil
}

func parsePrivateKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("not a PKCS#1 or PKCS#8 RSA private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("PEM block does not contain an RSA private key")
	}
	return key, nil
}

// installationAccessTokenResponse is the response body of GitHub's
// POST /app/installations/{id}/access_tokens.
type installationAccessTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

type githubAPIError struct {
	Message string `json:"message"`
}

// InstallationToken signs a fresh App JWT and exchanges it for an
// installation access token scoped to installationID. Callers needing
// this frequently should wrap it in InstallationTokenSource, which
// caches the result until shortly before expiry instead of minting a
// new token on every call.
func (a *AppAuthenticator) InstallationToken(ctx context.Context, installationID int64) (string, error) {
	jwt, err := a.signJWT(time.Now())
	if err != nil {
		return "", fmt.Errorf("githubauth: sign app jwt: %w", err)
	}

	base := a.BaseURL
	if base == "" {
		base = defaultAppBaseURL
	}
	url := fmt.Sprintf("%s/app/installations/%d/access_tokens", strings.TrimRight(base, "/"), installationID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	httpClient := a.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("githubauth: installation token request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		var parsed githubAPIError
		if json.Unmarshal(raw, &parsed) == nil && parsed.Message != "" {
			msg = parsed.Message
		}
		return "", fmt.Errorf("githubauth: installation token request failed (%d): %s", resp.StatusCode, msg)
	}

	var out installationAccessTokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("githubauth: decode installation token response: %w", err)
	}
	if out.Token == "" {
		return "", fmt.Errorf("githubauth: installation token response carried no token")
	}
	return out.Token, nil
}

// signJWT builds and signs (RS256) a GitHub App JWT for the given
// instant, per https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app.
// Implemented directly against the stdlib rather than pulling in a JWT
// library, consistent with this repo's existing dependency-light style
// (see go.mod) -- a GitHub App JWT is exactly three base64url segments
// with a fixed three-claim payload, not worth a dependency.
func (a *AppAuthenticator) signJWT(now time.Time) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-clockSkewAllowance).Unix(),
		"exp": now.Add(jwtTTL).Unix(),
		"iss": a.AppID,
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	signingInput := base64URLEncode(headerJSON) + "." + base64URLEncode(claimsJSON)

	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, a.PrivateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}

	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func base64URLEncode(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
