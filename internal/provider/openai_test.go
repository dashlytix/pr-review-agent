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

func openAITextResponse(text string) map[string]any {
	return map[string]any{
		"choices": []map[string]any{
			{"message": map[string]string{"role": "assistant", "content": text}},
		},
	}
}

func newTestOpenAIProvider(server *httptest.Server) *OpenAIProvider {
	p := NewOpenAIProvider("test-key", &http.Client{})
	p.BaseURL = server.URL
	return p
}

func TestOpenAIProvider_Assess_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("expected Bearer auth header, got %q", auth)
		}
		json.NewEncoder(w).Encode(openAITextResponse(validFindingsJSON))
	}))
	defer server.Close()

	p := newTestOpenAIProvider(server)
	findings, err := p.Assess(context.Background(), assess.AssessmentRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(findings) != 1 || findings[0].Category != "ci-failure" {
		t.Errorf("got %+v", findings)
	}
}

func TestOpenAIProvider_Assess_RepairsMalformedFirstResponse(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			json.NewEncoder(w).Encode(openAITextResponse("not json at all"))
			return
		}
		json.NewEncoder(w).Encode(openAITextResponse(validFindingsJSON))
	}))
	defer server.Close()

	p := newTestOpenAIProvider(server)
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

func TestOpenAIProvider_Assess_MalformedAfterRepairReturnsErrMalformed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(openAITextResponse("still not json"))
	}))
	defer server.Close()

	p := newTestOpenAIProvider(server)
	_, err := p.Assess(context.Background(), assess.AssessmentRequest{})
	if !errors.Is(err, assess.ErrMalformed) {
		t.Fatalf("expected errors.Is(err, assess.ErrMalformed), got: %v", err)
	}
}

func TestOpenAIProvider_Assess_APIErrorIsWrapped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": "rate limit exceeded"},
		})
	}))
	defer server.Close()

	p := newTestOpenAIProvider(server)
	_, err := p.Assess(context.Background(), assess.AssessmentRequest{})
	if err == nil {
		t.Fatal("expected an error for a 429 response")
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Errorf("expected the API error message to surface, got: %v", err)
	}
}

func TestOpenAIProvider_Answer_ReturnsRawText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body openAIRequest
		json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) != 2 || body.Messages[0].Role != "system" || !strings.Contains(body.Messages[0].Content, "answering a follow-up question") {
			t.Errorf("expected AnswerSystemPrompt as the system message, got: %+v", body.Messages)
		}
		if !strings.Contains(body.Messages[1].Content, "## Question\nwhy did this fail?") {
			t.Errorf("expected the question appended to the prompt, got: %q", body.Messages[1].Content)
		}
		json.NewEncoder(w).Encode(openAITextResponse("It fails because the nil check was removed."))
	}))
	defer server.Close()

	p := newTestOpenAIProvider(server)
	answer, err := p.Answer(context.Background(), assess.AssessmentRequest{}, "why did this fail?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answer != "It fails because the nil check was removed." {
		t.Errorf("answer = %q, want the raw model text with no JSON parsing applied", answer)
	}
}

func TestOpenAIProvider_Assess_DoesNotForceJSONObjectResponseFormat(t *testing.T) {
	// A top-level JSON array (what SystemPrompt now asks for) is
	// incompatible with response_format: {type: "json_object"}, which
	// requires an object. Confirm the request body doesn't set it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if _, present := body["response_format"]; present {
			t.Errorf("request body should not set response_format when the prompt asks for a JSON array, got: %v", body["response_format"])
		}
		json.NewEncoder(w).Encode(openAITextResponse(validFindingsJSON))
	}))
	defer server.Close()

	p := newTestOpenAIProvider(server)
	if _, err := p.Assess(context.Background(), assess.AssessmentRequest{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
