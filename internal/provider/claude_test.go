package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dimension/ai-ci-agent/internal/assess"
)

// claudeTextResponse builds the Anthropic Messages API response shape
// wrapping the given text as the model's reply.
func claudeTextResponse(text string) map[string]any {
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
	}
}

func newTestClaudeProvider(server *httptest.Server) *ClaudeProvider {
	p := NewClaudeProvider("test-key", &http.Client{})
	p.BaseURL = server.URL
	return p
}

const validFindingsJSON = `[{"file":"a.go","line":1,"category":"ci-failure","severity":"P1","comment":"the cause","suggested_fix":"fix it","confidence":"high","anchored":true}]`

func TestClaudeProvider_Assess_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key header, got %q", r.Header.Get("x-api-key"))
		}
		json.NewEncoder(w).Encode(claudeTextResponse(validFindingsJSON))
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	findings, err := p.Assess(context.Background(), assess.AssessmentRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 1 || findings[0].Category != "ci-failure" {
		t.Errorf("got %+v", findings)
	}
}

func TestClaudeProvider_Assess_RepairsMalformedFirstResponse(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			json.NewEncoder(w).Encode(claudeTextResponse("not json at all, sorry"))
			return
		}
		json.NewEncoder(w).Encode(claudeTextResponse(validFindingsJSON))
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	findings, err := p.Assess(context.Background(), assess.AssessmentRequest{})
	if err != nil {
		t.Fatalf("expected the repair attempt to succeed, got error: %v", err)
	}
	if len(findings) != 1 {
		t.Errorf("got %+v", findings)
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 calls (initial + one repair), got %d", calls)
	}
}

func TestClaudeProvider_Assess_MalformedAfterRepairReturnsErrMalformed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(claudeTextResponse("still not json"))
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	_, err := p.Assess(context.Background(), assess.AssessmentRequest{})
	if !errors.Is(err, assess.ErrMalformed) {
		t.Fatalf("expected errors.Is(err, assess.ErrMalformed), got: %v", err)
	}
}

func TestClaudeProvider_Assess_APIErrorIsWrapped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": "invalid x-api-key"},
		})
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	_, err := p.Assess(context.Background(), assess.AssessmentRequest{})
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("expected the Anthropic error message to surface, got: %v", err)
	}
}
