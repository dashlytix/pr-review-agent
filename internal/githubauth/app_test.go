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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testKey generates a fresh RSA key pair for tests -- nothing here
// requires a real GitHub App or its real private key; the JWT-signing
// and installation-token-exchange logic is correct or not independent
// of whose key is used.
func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test RSA key: %v", err)
	}
	return key
}

func testKeyPEM(t *testing.T, key *rsa.PrivateKey) []byte {
	t.Helper()
	der := x509.MarshalPKCS1PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
}

func TestNewAppAuthenticator_ParsesPKCS1PEM(t *testing.T) {
	key := testKey(t)
	a, err := NewAppAuthenticator("12345", testKeyPEM(t, key))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.AppID != "12345" {
		t.Errorf("AppID = %q, want 12345", a.AppID)
	}
	if a.PrivateKey.N.Cmp(key.N) != 0 {
		t.Error("parsed key does not match the original")
	}
}

func TestNewAppAuthenticator_RejectsGarbage(t *testing.T) {
	if _, err := NewAppAuthenticator("12345", []byte("not a pem block")); err == nil {
		t.Fatal("expected an error for a non-PEM input")
	}
}

// decodeJWT splits a signed JWT into its header/claims maps and raw
// signing input + signature, without validating anything -- tests use
// this to inspect claims and independently re-verify the signature.
func decodeJWT(t *testing.T, token string) (header, claims map[string]any, signingInput string, signature []byte) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d segments, want 3 (header.claims.signature)", len(parts))
	}

	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("decode header: %v", err)
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}

	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}

	return header, claims, parts[0] + "." + parts[1], sig
}

func TestSignJWT_ProducesValidRS256TokenWithExpectedClaims(t *testing.T) {
	key := testKey(t)
	a := &AppAuthenticator{AppID: "999", PrivateKey: key}
	now := time.Now()

	token, err := a.signJWT(now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	header, claims, signingInput, sig := decodeJWT(t, token)

	if header["alg"] != "RS256" || header["typ"] != "JWT" {
		t.Errorf("header = %+v, want alg=RS256, typ=JWT", header)
	}
	if claims["iss"] != "999" {
		t.Errorf("iss claim = %v, want 999", claims["iss"])
	}

	iat, _ := claims["iat"].(float64)
	exp, _ := claims["exp"].(float64)
	if exp <= iat {
		t.Errorf("exp (%v) must be after iat (%v)", exp, iat)
	}
	if got := time.Unix(int64(iat), 0); got.After(now) {
		t.Errorf("iat = %v, want it backdated for clock-skew allowance (not after %v)", got, now)
	}
	if wantMax := now.Add(jwtTTL + time.Second); time.Unix(int64(exp), 0).After(wantMax) {
		t.Errorf("exp = %v, exceeds the intended TTL past %v", time.Unix(int64(exp), 0), wantMax)
	}

	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Errorf("signature does not verify against the signing key's public half: %v", err)
	}
}

func TestSignJWT_DifferentKeyFailsVerification(t *testing.T) {
	signingKey := testKey(t)
	otherKey := testKey(t)
	a := &AppAuthenticator{AppID: "999", PrivateKey: signingKey}

	token, err := a.signJWT(time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, _, signingInput, sig := decodeJWT(t, token)

	digest := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(&otherKey.PublicKey, crypto.SHA256, digest[:], sig); err == nil {
		t.Error("expected verification against the wrong public key to fail")
	}
}

func TestInstallationToken_ExchangesJWTForInstallationToken(t *testing.T) {
	key := testKey(t)
	var gotPath, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"token":      "ghs_installation_token",
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	}))
	defer server.Close()

	a := &AppAuthenticator{AppID: "42", PrivateKey: key, BaseURL: server.URL}
	tok, err := a.InstallationToken(context.Background(), 777)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "ghs_installation_token" {
		t.Errorf("token = %q, want ghs_installation_token", tok)
	}
	if gotPath != "/app/installations/777/access_tokens" {
		t.Errorf("request path = %q, want /app/installations/777/access_tokens", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") || len(strings.Split(strings.TrimPrefix(gotAuth, "Bearer "), ".")) != 3 {
		t.Errorf("Authorization header = %q, want a Bearer-prefixed three-segment JWT", gotAuth)
	}
}

func TestInstallationToken_NonOKStatusIsAnError(t *testing.T) {
	key := testKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"message": "Bad credentials"})
	}))
	defer server.Close()

	a := &AppAuthenticator{AppID: "42", PrivateKey: key, BaseURL: server.URL}
	if _, err := a.InstallationToken(context.Background(), 1); err == nil {
		t.Fatal("expected an error for a non-2xx response")
	} else if !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("error = %v, want it to surface the API's message", err)
	}
}

func TestInstallationToken_EmptyTokenInResponseIsAnError(t *testing.T) {
	key := testKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": ""})
	}))
	defer server.Close()

	a := &AppAuthenticator{AppID: "42", PrivateKey: key, BaseURL: server.URL}
	if _, err := a.InstallationToken(context.Background(), 1); err == nil {
		t.Fatal("expected an error when the response carries no token")
	}
}
