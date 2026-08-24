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

// claudeBlocksResponse builds a response made of arbitrary content
// blocks, so tests can reproduce what reasoning-capable models actually
// return: a "thinking" block ahead of the real answer.
func claudeBlocksResponse(blocks ...map[string]string) map[string]any {
	return map[string]any{"content": blocks}
}

// A reasoning model prepends a "thinking" block, so the findings array
// arrives in a later block. Reading only content[0] used to yield an
// empty string, which failed to parse and then sent an empty user
// message to the repair call — a 400 that masked the real problem.
func TestClaudeProvider_Assess_SkipsThinkingBlocks(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		json.NewEncoder(w).Encode(claudeBlocksResponse(
			map[string]string{"type": "thinking", "thinking": "deliberating"},
			map[string]string{"type": "text", "text": validFindingsJSON},
		))
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	findings, err := p.Assess(context.Background(), assess.AssessmentRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("made %d call(s), want 1 — no repair attempt should be needed", got)
	}
	if len(findings) != 1 || findings[0].Category != "ci-failure" {
		t.Errorf("got %+v, want the finding from the text block", findings)
	}
}

// The API may split one answer across several text blocks.
func TestClaudeProvider_Assess_JoinsMultipleTextBlocks(t *testing.T) {
	half := len(validFindingsJSON) / 2
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(claudeBlocksResponse(
			map[string]string{"type": "text", "text": validFindingsJSON[:half]},
			map[string]string{"type": "text", "text": validFindingsJSON[half:]},
		))
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	findings, err := p.Assess(context.Background(), assess.AssessmentRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 1 || findings[0].Category != "ci-failure" {
		t.Errorf("got %+v, want one finding reassembled from both text blocks", findings)
	}
}

const validReviewResultJSON = `{"summary":"Fixes an off-by-one in the pagination helper.","findings":[{"file":"a.go","line":1,"category":"correctness","severity":"P2","comment":"off-by-one","suggested_fix":"use <=","confidence":"medium","anchored":true}]}`

func TestClaudeProvider_Review_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(claudeTextResponse(validReviewResultJSON))
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	result, err := p.Review(context.Background(), assess.AssessmentRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Summary == "" {
		t.Error("expected a non-empty Summary")
	}
	if len(result.Findings) != 1 || result.Findings[0].Category != "correctness" {
		t.Errorf("got %+v", result.Findings)
	}
}

// An empty findings array is the common, valid "no issues found" result
// for the review path — unlike Assess, it must not be treated as
// malformed. Summary is still mandatory even when findings is empty.
func TestClaudeProvider_Review_EmptyFindingsArrayIsValid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(claudeTextResponse(`{"summary":"Adds a health-check endpoint.","findings":[]}`))
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	result, err := p.Review(context.Background(), assess.AssessmentRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Findings) != 0 {
		t.Errorf("expected 0 findings, got %+v", result.Findings)
	}
	if result.Summary == "" {
		t.Error("expected a non-empty Summary even with zero findings")
	}
}

func TestClaudeProvider_Review_RepairsMalformedFirstResponse(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			json.NewEncoder(w).Encode(claudeTextResponse("not json at all, sorry"))
			return
		}
		json.NewEncoder(w).Encode(claudeTextResponse(validReviewResultJSON))
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	result, err := p.Review(context.Background(), assess.AssessmentRequest{})
	if err != nil {
		t.Fatalf("expected the repair attempt to succeed, got error: %v", err)
	}
	if len(result.Findings) != 1 {
		t.Errorf("got %+v", result.Findings)
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 calls (initial + one repair), got %d", calls)
	}
}

// A response carrying no usable text must surface as an error rather than
// handing "" to the repair call, which the Messages API rejects with a
// 400 ("user messages must have non-empty content").
func TestClaudeProvider_Assess_NoTextBlocksDoesNotAttemptRepair(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		json.NewEncoder(w).Encode(claudeBlocksResponse(
			map[string]string{"type": "thinking", "thinking": ""},
		))
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	if _, err := p.Assess(context.Background(), assess.AssessmentRequest{}); err == nil {
		t.Fatal("expected an error when the response carries no text blocks")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("made %d call(s), want 1 — an empty response must not be sent back for repair", got)
	}
}
