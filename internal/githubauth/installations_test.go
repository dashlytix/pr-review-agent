package githubauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestListInstallations_PaginatesAcrossPages(t *testing.T) {
	key := testKey(t)
	var gotAuths []string
	page1 := make([]map[string]any, installationsPerPage)
	for i := range page1 {
		page1[i] = map[string]any{"id": i + 1, "account": map[string]string{"login": "acme", "type": "Organization"}, "repository_selection": "all"}
	}
	page2 := []map[string]any{
		{"id": 9999, "account": map[string]string{"login": "someone", "type": "User"}, "repository_selection": "selected"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuths = append(gotAuths, r.Header.Get("Authorization"))
		if r.URL.Path != "/app/installations" {
			t.Errorf("path = %q, want /app/installations", r.URL.Path)
		}
		if r.URL.Query().Get("page") == "2" {
			json.NewEncoder(w).Encode(page2)
			return
		}
		json.NewEncoder(w).Encode(page1)
	}))
	defer server.Close()

	a := &AppAuthenticator{AppID: "42", PrivateKey: key, BaseURL: server.URL}
	got, err := a.ListInstallations(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != installationsPerPage+1 {
		t.Fatalf("len(got) = %d, want %d", len(got), installationsPerPage+1)
	}
	if got[len(got)-1].Account.Login != "someone" || got[len(got)-1].RepositorySelection != "selected" {
		t.Errorf("last installation = %+v, want the second page's entry", got[len(got)-1])
	}
	for _, auth := range gotAuths {
		if !strings.HasPrefix(auth, "Bearer ") || len(strings.Split(strings.TrimPrefix(auth, "Bearer "), ".")) != 3 {
			t.Errorf("Authorization = %q, want a Bearer-prefixed three-segment App JWT", auth)
		}
	}
}

func TestListInstallations_NonOKStatusIsAnError(t *testing.T) {
	key := testKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"message": "App suspended"})
	}))
	defer server.Close()

	a := &AppAuthenticator{AppID: "42", PrivateKey: key, BaseURL: server.URL}
	if _, err := a.ListInstallations(context.Background()); err == nil {
		t.Fatal("expected an error for a non-2xx response")
	} else if !strings.Contains(err.Error(), "App suspended") {
		t.Errorf("error = %v, want it to surface the API's message", err)
	}
}

func TestListInstallationRepositories_UsesInstallationTokenNotAppJWT(t *testing.T) {
	key := testKey(t)
	var tokenExchangeCalls int
	var reposCallAuth string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/installations/777/access_tokens":
			tokenExchangeCalls++
			json.NewEncoder(w).Encode(map[string]string{
				"token":      "ghs_installation_token",
				"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
			})
		case r.URL.Path == "/installation/repositories":
			reposCallAuth = r.Header.Get("Authorization")
			json.NewEncoder(w).Encode(map[string]any{
				"total_count": 1,
				"repositories": []map[string]any{
					{"full_name": "acme/widgets", "private": true, "html_url": "https://github.com/acme/widgets"},
				},
			})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	a := &AppAuthenticator{AppID: "42", PrivateKey: key, BaseURL: server.URL}
	got, err := a.ListInstallationRepositories(context.Background(), 777)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tokenExchangeCalls != 1 {
		t.Errorf("token exchange called %d times, want exactly 1", tokenExchangeCalls)
	}
	if reposCallAuth != "Bearer ghs_installation_token" {
		t.Errorf("repositories call Authorization = %q, want the minted installation token, not an App JWT", reposCallAuth)
	}
	if len(got) != 1 || got[0].FullName != "acme/widgets" || !got[0].Private {
		t.Errorf("got = %+v, want one private acme/widgets repo", got)
	}
}

func TestListInstallationRepositories_PaginatesAcrossPages(t *testing.T) {
	key := testKey(t)
	page1 := make([]map[string]any, installationsPerPage)
	for i := range page1 {
		page1[i] = map[string]any{"full_name": fmt.Sprintf("acme/repo-%d", i)}
	}
	page2 := []map[string]any{{"full_name": "acme/last-one"}}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/installations/777/access_tokens" {
			json.NewEncoder(w).Encode(map[string]string{"token": "t", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
			return
		}
		repos := page1
		if r.URL.Query().Get("page") == "2" {
			repos = page2
		}
		json.NewEncoder(w).Encode(map[string]any{"total_count": len(repos), "repositories": repos})
	}))
	defer server.Close()

	a := &AppAuthenticator{AppID: "42", PrivateKey: key, BaseURL: server.URL}
	got, err := a.ListInstallationRepositories(context.Background(), 777)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != installationsPerPage+1 {
		t.Fatalf("len(got) = %d, want %d", len(got), installationsPerPage+1)
	}
	if got[len(got)-1].FullName != "acme/last-one" {
		t.Errorf("last repo = %q, want acme/last-one", got[len(got)-1].FullName)
	}
}

func TestListInstallationRepositories_TokenExchangeFailurePropagates(t *testing.T) {
	key := testKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"message": "Bad credentials"})
	}))
	defer server.Close()

	a := &AppAuthenticator{AppID: "42", PrivateKey: key, BaseURL: server.URL}
	if _, err := a.ListInstallationRepositories(context.Background(), 777); err == nil {
		t.Fatal("expected an error when the installation-token exchange itself fails")
	}
}
