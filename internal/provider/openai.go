package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/dimension/ai-ci-agent/internal/assess"
)

const defaultOpenAIAPIURL = "https://api.openai.com/v1/chat/completions"

// openAIAPIPath is the canonical chat-completions path appended to any
// base-URL override.
const openAIAPIPath = "/v1/chat/completions"

// defaultOpenAIModel is used unless Config.Model overrides it.
const defaultOpenAIModel = "gpt-4o-mini"

// OpenAIProvider talks to any OpenAI-compatible chat completions API —
// not just OpenAI itself. BaseURL is overridable so the same code path
// works against a gateway like OpenRouter, which re-exposes many
// providers behind one OpenAI-shaped endpoint.
type OpenAIProvider struct {
	APIKey  string
	Model   string
	BaseURL string
	HTTP    *http.Client
}

func NewOpenAIProvider(apiKey string, httpClient *http.Client) *OpenAIProvider {
	return &OpenAIProvider{
		APIKey:  apiKey,
		Model:   defaultOpenAIModel,
		BaseURL: defaultOpenAIAPIURL,
		HTTP:    httpClient,
	}
}

type openAIRequest struct {
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Assess mirrors ClaudeProvider.Assess: same prompt, same parse/repair
// flow, only the wire format differs. Keeping both providers' Assess
// methods structurally identical is what makes "provider parity" (§6.1)
// something the eval harness can actually verify.
func (p *OpenAIProvider) Assess(ctx context.Context, req AssessmentRequest) ([]Assessment, error) {
	prompt := assess.BuildPrompt(req)

	raw, err := p.complete(ctx, assess.SystemPrompt, prompt)
	if err != nil {
		return nil, fmt.Errorf("openai: assess call failed: %w", err)
	}

	findings, parseErr := assess.ParseAssessments(raw)
	if parseErr == nil {
		assess.ValidateAnchors(req, findings)
		return findings, nil
	}

	repaired, repairErr := p.complete(ctx, assess.RepairSystemPrompt, raw)
	if repairErr != nil {
		return nil, fmt.Errorf("%w: original parse error: %v; repair call failed: %v", assess.ErrMalformed, parseErr, repairErr)
	}

	findings, err = assess.ParseAssessments(repaired)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", assess.ErrMalformed, err)
	}

	assess.ValidateAnchors(req, findings)
	return findings, nil
}

// Review mirrors OpenAIProvider.Assess but drives the plain PR-review
// path: a different system prompt (assess.ReviewSystemPrompt) and parse
// function (assess.ParseReview), since there's no mandatory "ci-failure"
// category here, and the response carries a summary alongside its
// findings.
func (p *OpenAIProvider) Review(ctx context.Context, req AssessmentRequest) (ReviewResult, error) {
	prompt := assess.BuildReviewPrompt(req)

	raw, err := p.complete(ctx, assess.ReviewSystemPrompt, prompt)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("openai: review call failed: %w", err)
	}

	result, parseErr := assess.ParseReview(raw)
	if parseErr == nil {
		assess.ValidateAnchors(req, result.Findings)
		return result, nil
	}

	repaired, repairErr := p.complete(ctx, assess.ReviewRepairSystemPrompt, raw)
	if repairErr != nil {
		return ReviewResult{}, fmt.Errorf("%w: original parse error: %v; repair call failed: %v", assess.ErrMalformed, parseErr, repairErr)
	}

	result, err = assess.ParseReview(repaired)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("%w: %v", assess.ErrMalformed, err)
	}

	assess.ValidateAnchors(req, result.Findings)
	return result, nil
}

// Answer drives the Slack Q&A path: a free-form question about the same
// diff/files context Review uses, answered as plain text. Unlike
// Assess/Review there's no JSON contract to parse, so a successful call
// returns the model's raw text directly -- no repair-retry step.
func (p *OpenAIProvider) Answer(ctx context.Context, req AssessmentRequest, question string) (string, error) {
	prompt := assess.BuildAnswerPrompt(req, question)

	text, err := p.complete(ctx, assess.AnswerSystemPrompt, prompt)
	if err != nil {
		return "", fmt.Errorf("openai: answer call failed: %w", err)
	}
	return text, nil
}

func (p *OpenAIProvider) complete(ctx context.Context, system, user string) (string, error) {
	body := openAIRequest{
		Model: p.Model,
		Messages: []openAIMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+p.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.HTTP.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var parsed openAIResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("invalid response body: %w", err)
	}
	if resp.StatusCode >= 300 {
		if parsed.Error != nil {
			return "", fmt.Errorf("api error (%d): %s", resp.StatusCode, parsed.Error.Message)
		}
		return "", fmt.Errorf("api error (%d)", resp.StatusCode)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("empty response choices")
	}
	return parsed.Choices[0].Message.Content, nil
}
