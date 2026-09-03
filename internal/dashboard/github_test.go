package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExchangeCode_Success(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/login/oauth/access_token" {
			t.Errorf("path = %q, want /login/oauth/access_token", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		json.NewEncoder(w).Encode(map[string]string{"access_token": "gho_abc123"})
	}))
	defer server.Close()

	tok, err := exchangeCode(context.Background(), server.Client(), server.URL, "client-id", "client-secret", "the-code", "https://dashboard/auth/callback")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "gho_abc123" {
		t.Errorf("token = %q, want gho_abc123", tok)
	}
	for _, want := range []string{"client_id=client-id", "code=the-code", "redirect_uri="} {
		if !strings.Contains(gotBody, want) {
			t.Errorf("request body = %q, want it to contain %q", gotBody, want)
		}
	}
}

func TestExchangeCode_OAuthErrorInBodyIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code", "error_description": "expired"})
	}))
	defer server.Close()

	if _, err := exchangeCode(context.Background(), server.Client(), server.URL, "id", "secret", "stale-code", "https://x/callback"); err == nil {
		t.Fatal("expected an error when the response body carries an oauth error")
	} else if !strings.Contains(err.Error(), "bad_verification_code") {
		t.Errorf("error = %v, want it to surface the oauth error", err)
	}
}

func TestExchangeCode_NonOKStatusIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	if _, err := exchangeCode(context.Background(), server.Client(), server.URL, "id", "secret", "code", "https://x/callback"); err == nil {
		t.Fatal("expected an error for a non-2xx response")
	}
}

func TestGithubLogin_ReturnsLogin(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(map[string]string{"login": "octocat"})
	}))
	defer server.Close()

	login, err := githubLogin(context.Background(), server.Client(), server.URL, "gho_abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if login != "octocat" {
		t.Errorf("login = %q, want octocat", login)
	}
	if gotAuth != "Bearer gho_abc123" {
		t.Errorf("Authorization = %q, want the user's own token", gotAuth)
	}
}

func TestIsOrgAdmin_ActiveAdminReturnsTrue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/memberships/orgs/dashlytix" {
			t.Errorf("path = %q, want /user/memberships/orgs/dashlytix", r.URL.Path)
		}
		json.NewEncoder(w).Encode(orgMembership{Role: "admin", State: "active"})
	}))
	defer server.Close()

	admin, err := isOrgAdmin(context.Background(), server.Client(), server.URL, "gho_abc", "dashlytix")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !admin {
		t.Error("expected an active admin membership to report true")
	}
}

func TestIsOrgAdmin_PlainMemberReturnsFalse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(orgMembership{Role: "member", State: "active"})
	}))
	defer server.Close()

	admin, err := isOrgAdmin(context.Background(), server.Client(), server.URL, "gho_abc", "dashlytix")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if admin {
		t.Error("expected a plain member to report false")
	}
}

func TestIsOrgAdmin_PendingAdminReturnsFalse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(orgMembership{Role: "admin", State: "pending"})
	}))
	defer server.Close()

	admin, err := isOrgAdmin(context.Background(), server.Client(), server.URL, "gho_abc", "dashlytix")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if admin {
		t.Error("expected a pending (not yet accepted) admin invite to report false")
	}
}

func TestIsOrgAdmin_NotFoundReturnsFalseNotError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	admin, err := isOrgAdmin(context.Background(), server.Client(), server.URL, "gho_abc", "dashlytix")
	if err != nil {
		t.Fatalf("expected a 404 (not a member) to be nil-error, false, got error: %v", err)
	}
	if admin {
		t.Error("expected 404 to report false")
	}
}

func TestIsOrgAdmin_ServerErrorIsAnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"message": "internal error"})
	}))
	defer server.Close()

	admin, err := isOrgAdmin(context.Background(), server.Client(), server.URL, "gho_abc", "dashlytix")
	if err == nil {
		t.Fatal("expected a 500 to be returned as an error, not silently treated as non-admin (fail closed, not fail open)")
	}
	if admin {
		t.Error("admin must be false alongside the error")
	}
}
