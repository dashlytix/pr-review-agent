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
		Model:   "gpt-4o-mini",
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
