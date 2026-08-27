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

func TestClaudeProvider_Assess_FallsBackOnTier1_402(t *testing.T) {
	var tier2Calls int32
	var tier2APIKey string

	tier1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Matches the real exe-llm failure mode: a plain-text error body,
		// not JSON.
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte("LLM credits exhausted; credits refresh over time"))
	}))
	defer tier1.Close()

	tier2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tier2Calls, 1)
		tier2APIKey = r.Header.Get("x-api-key")
		json.NewEncoder(w).Encode(claudeTextResponse(validFindingsJSON))
	}))
	defer tier2.Close()

	p := NewClaudeProvider("real-anthropic-key", &http.Client{})
	p.BaseURL = tier1.URL
	p.APIKey = "implicit"
	p.FallbackBaseURL = tier2.URL
	p.FallbackAPIKey = "real-anthropic-key"

	findings, err := p.Assess(context.Background(), assess.AssessmentRequest{})
	if err != nil {
		t.Fatalf("expected the fallback tier to serve the request, got error: %v", err)
	}
	if len(findings) != 1 || findings[0].Category != "ci-failure" {
		t.Errorf("got %+v", findings)
	}
	if tier2Calls != 1 {
		t.Errorf("expected exactly 1 call to tier 2, got %d", tier2Calls)
	}
	if tier2APIKey != "real-anthropic-key" {
		t.Errorf("tier 2 saw x-api-key %q, want the fallback (real) key, not tier 1's implicit sentinel", tier2APIKey)
	}
}

func TestClaudeProvider_Assess_DoesNotFallBackOnContentError(t *testing.T) {
	var tier1Calls, tier2Calls int32

	tier1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tier1Calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": "prompt is too long: context length exceeded"},
		})
	}))
	defer tier1.Close()

	tier2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tier2Calls, 1)
		json.NewEncoder(w).Encode(claudeTextResponse(validFindingsJSON))
	}))
	defer tier2.Close()

	p := NewClaudeProvider("real-anthropic-key", &http.Client{})
	p.BaseURL = tier1.URL
	p.APIKey = "implicit"
	p.FallbackBaseURL = tier2.URL
	p.FallbackAPIKey = "real-anthropic-key"

	_, err := p.Assess(context.Background(), assess.AssessmentRequest{})
	if err == nil {
		t.Fatal("expected the 400 content-level error to surface, not be masked by a fallback attempt")
	}
	if !strings.Contains(err.Error(), "context length exceeded") {
		t.Errorf("expected tier 1's real error message to surface, got: %v", err)
	}
	if tier2Calls != 0 {
		t.Errorf("expected tier 2 to never be called for a content-level error, got %d calls", tier2Calls)
	}
	if tier1Calls != 1 {
		t.Errorf("expected exactly 1 call to tier 1, got %d", tier1Calls)
	}
}

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

func TestClaudeProvider_Answer_ReturnsRawText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body claudeRequest
		json.NewDecoder(r.Body).Decode(&body)
		if !strings.Contains(body.System, "answering a follow-up question") {
			t.Errorf("expected AnswerSystemPrompt to be sent, got system prompt: %q", body.System)
		}
		if len(body.Messages) != 1 || !strings.Contains(body.Messages[0].Content, "## Question\nwhy did this fail?") {
			t.Errorf("expected the question appended to the prompt, got: %+v", body.Messages)
		}
		json.NewEncoder(w).Encode(claudeTextResponse("It fails because the nil check was removed."))
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	answer, err := p.Answer(context.Background(), assess.AssessmentRequest{}, "why did this fail?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answer != "It fails because the nil check was removed." {
		t.Errorf("answer = %q, want the raw model text with no JSON parsing applied", answer)
	}
}

func TestClaudeProvider_Answer_APIErrorIsWrapped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": "invalid x-api-key"},
		})
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	_, err := p.Answer(context.Background(), assess.AssessmentRequest{}, "why did this fail?")
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if !strings.Contains(err.Error(), "invalid x-api-key") {
		t.Errorf("expected the Anthropic error message to surface, got: %v", err)
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

// reviewRequestWithDiff supplies the diff context validReviewResultJSON's
// finding is anchored against (a.go:1), so RefineAssessments' grounding
// check has real evidence to confirm rather than dropping the finding.
func reviewRequestWithDiff() assess.AssessmentRequest {
	return assess.AssessmentRequest{
		Files: map[string]string{"a.go": "@@ -1 +1 @@\n+package a"},
	}
}

func TestClaudeProvider_Review_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(claudeTextResponse(validReviewResultJSON))
	}))
	defer server.Close()

	p := newTestClaudeProvider(server)
	result, err := p.Review(context.Background(), reviewRequestWithDiff())
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
	result, err := p.Review(context.Background(), reviewRequestWithDiff())
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
