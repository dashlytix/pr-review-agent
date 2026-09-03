package dashboard

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dimension/ai-ci-agent/internal/githubauth"
)

// testHandler builds a Handler wired against a stub GitHub API/OAuth
// server (mux) and a fresh codec, ready for ServeHTTP via a *http.ServeMux.
func testHandler(t *testing.T, mux http.Handler) (*Handler, *http.ServeMux, *httptest.Server) {
	t.Helper()
	ghServer := httptest.NewServer(mux)
	t.Cleanup(ghServer.Close)

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	app, err := githubauth.NewAppAuthenticator("42", pemBytes)
	if err != nil {
		t.Fatalf("build app authenticator: %v", err)
	}
	app.BaseURL = ghServer.URL

	codec, err := NewSessionCodec(testSessionKey())
	if err != nil {
		t.Fatalf("build session codec: %v", err)
	}

	h := &Handler{
		App:          app,
		Codec:        codec,
		Org:          "dashlytix",
		AppSlug:      "dashlytix-pr-review-agent",
		ClientID:     "client-id",
		ClientSecret: "client-secret",
		BaseURL:      "https://dashboard.example.com",
		HTTP:         ghServer.Client(),
		APIBaseURL:   ghServer.URL,
		OAuthBaseURL: ghServer.URL,
	}
	serveMux := http.NewServeMux()
	h.Register(serveMux)
	return h, serveMux, ghServer
}

// adminGitHubStub serves the handful of endpoints the admin-session
// paths need: org membership as an active admin, one installation with
// one repo.
func adminGitHubStub(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/user/memberships/orgs/dashlytix", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"role": "admin", "state": "active"})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"login": "octocat"})
	})
	mux.HandleFunc("/app/installations", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			json.NewEncoder(w).Encode([]any{})
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"id": 1, "account": map[string]string{"login": "dashlytix", "type": "Organization"}, "repository_selection": "selected", "html_url": "https://github.com/x"},
		})
	})
	mux.HandleFunc("/app/installations/1/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"token": "ghs_x", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
	})
	mux.HandleFunc("/installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			json.NewEncoder(w).Encode(map[string]any{"total_count": 0, "repositories": []any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "repositories": []map[string]any{{"full_name": "dashlytix/dash-ai-agent"}}})
	})
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"access_token": "gho_test"})
	})
	return mux
}

func validAdminSessionCookie(t *testing.T, h *Handler) *http.Cookie {
	t.Helper()
	sealed, err := h.Codec.seal(session{Login: "octocat", UserToken: "gho_test", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("seal session: %v", err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: sealed}
}

func TestHandleIndex_NoCookieShowsLogin(t *testing.T) {
	_, mux, _ := testHandler(t, adminGitHubStub(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Sign in with GitHub") {
		t.Error("expected the login page to render")
	}
}

func TestHandleIndex_InvalidCookieShowsLoginAndClearsCookie(t *testing.T) {
	h, mux, _ := testHandler(t, adminGitHubStub(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "garbage-not-a-real-session"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "Sign in with GitHub") {
		t.Error("expected the login page to render for an invalid cookie")
	}
	assertCookieCleared(t, rec, sessionCookieName)
	_ = h
}

func TestHandleIndex_ExpiredCookieShowsLoginAndClearsCookie(t *testing.T) {
	h, mux, _ := testHandler(t, adminGitHubStub(t))
	sealed, err := h.Codec.seal(session{Login: "octocat", UserToken: "gho_test", ExpiresAt: time.Now().Add(-time.Minute)})
	if err != nil {
		t.Fatalf("seal session: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sealed})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "Sign in with GitHub") {
		t.Error("expected the login page to render for an expired cookie")
	}
	assertCookieCleared(t, rec, sessionCookieName)
}

func TestHandleIndex_ValidSessionButNoLongerAdminShowsLoginAndClearsCookie(t *testing.T) {
	stub := http.NewServeMux()
	stub.HandleFunc("/user/memberships/orgs/dashlytix", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"role": "member", "state": "active"})
	})
	h, mux, _ := testHandler(t, stub)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(validAdminSessionCookie(t, h))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "not an active admin") {
		t.Errorf("expected a not-an-admin message, got body: %s", rec.Body.String())
	}
	assertCookieCleared(t, rec, sessionCookieName)
}

func TestHandleIndex_ValidAdminSessionRendersInstallations(t *testing.T) {
	h, mux, _ := testHandler(t, adminGitHubStub(t))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(validAdminSessionCookie(t, h))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "dashlytix") || !strings.Contains(body, "dashlytix/dash-ai-agent") {
		t.Errorf("expected the installation and its repo to appear, got: %s", body)
	}
	if !strings.Contains(body, "dashlytix-pr-review-agent/installations/new") {
		t.Error("expected the install link to reference the configured App slug")
	}
}

func TestHandleIndex_MembershipCheckFailureIs502(t *testing.T) {
	stub := http.NewServeMux()
	stub.HandleFunc("/user/memberships/orgs/dashlytix", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	h, mux, _ := testHandler(t, stub)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(validAdminSessionCookie(t, h))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d (fail closed on a GitHub outage, not silently show the login page)", rec.Code, http.StatusBadGateway)
	}
}

func TestHandleLogin_SetsStateCookieAndRedirectsToAuthorize(t *testing.T) {
	_, mux, ghServer := testHandler(t, adminGitHubStub(t))
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	if !strings.HasPrefix(rec.Header().Get("Location"), ghServer.URL+"/login/oauth/authorize") {
		t.Errorf("Location = %q, want it to point at the oauth authorize endpoint", rec.Header().Get("Location"))
	}
	if loc.Query().Get("client_id") != "client-id" {
		t.Errorf("client_id = %q, want client-id", loc.Query().Get("client_id"))
	}
	if loc.Query().Get("redirect_uri") != "https://dashboard.example.com/auth/callback" {
		t.Errorf("redirect_uri = %q, want the configured BaseURL + /auth/callback", loc.Query().Get("redirect_uri"))
	}
	if loc.Query().Get("state") == "" {
		t.Error("expected a non-empty state parameter")
	}

	var stateCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauthStateCookieName {
			stateCookie = c
		}
	}
	if stateCookie == nil || stateCookie.Value != loc.Query().Get("state") {
		t.Error("expected the state cookie to match the state query param")
	}
}

func TestHandleCallback_MismatchedStateRejectedWithNoSessionSet(t *testing.T) {
	_, mux, _ := testHandler(t, adminGitHubStub(t))
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=wrong&code=abc", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookieName, Value: "right"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Errorf("Location = %q, want an error redirect", rec.Header().Get("Location"))
	}
	assertNoCookieSet(t, rec, sessionCookieName)
}

func TestHandleCallback_NonAdminRejectedWithNoSessionSet(t *testing.T) {
	stub := http.NewServeMux()
	stub.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"access_token": "gho_test"})
	})
	stub.HandleFunc("/user/memberships/orgs/dashlytix", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"role": "member", "state": "active"})
	})
	_, mux, _ := testHandler(t, stub)

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=s&code=abc", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookieName, Value: "s"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Errorf("Location = %q, want an error redirect for a non-admin", rec.Header().Get("Location"))
	}
	assertNoCookieSet(t, rec, sessionCookieName)
}

func TestHandleCallback_AdminSetsSessionCookieAndRedirectsToIndex(t *testing.T) {
	_, mux, _ := testHandler(t, adminGitHubStub(t))

	req := httptest.NewRequest(http.MethodGet, "/auth/callback?state=s&code=abc", nil)
	req.AddCookie(&http.Cookie{Name: oauthStateCookieName, Value: "s"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("status/location = %d/%q, want 302 to /", rec.Code, rec.Header().Get("Location"))
	}
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected a session cookie to be set for a verified admin")
	}
}

func TestHandleLogout_ClearsCookieAndRedirects(t *testing.T) {
	_, mux, _ := testHandler(t, adminGitHubStub(t))
	req := httptest.NewRequest(http.MethodGet, "/logout", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("status/location = %d/%q, want 302 to /", rec.Code, rec.Header().Get("Location"))
	}
	assertCookieCleared(t, rec, sessionCookieName)
}

func assertCookieCleared(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			if c.MaxAge >= 0 {
				t.Errorf("cookie %s MaxAge = %d, want negative (cleared)", name, c.MaxAge)
			}
			return
		}
	}
	t.Errorf("expected a Set-Cookie clearing %s, found none", name)
}

func assertNoCookieSet(t *testing.T, rec *httptest.ResponseRecorder, name string) {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.Value != "" && c.MaxAge >= 0 {
			t.Errorf("expected no %s cookie to be set, found one with a value", name)
		}
	}
}
