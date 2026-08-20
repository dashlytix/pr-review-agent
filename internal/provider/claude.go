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

const defaultClaudeAPIURL = "https://api.anthropic.com/v1/messages"

// ClaudeProvider talks to the Anthropic Messages API. It is the default
// provider (§4.1: llm-provider defaults to "claude"). BaseURL is
// overridable so tests can point it at an httptest.Server, mirroring
// OpenAIProvider.
type ClaudeProvider struct {
	APIKey  string
	Model   string
	BaseURL string
	HTTP    *http.Client
}

func NewClaudeProvider(apiKey string, httpClient *http.Client) *ClaudeProvider {
	return &ClaudeProvider{
		APIKey:  apiKey,
		Model:   "claude-sonnet-5",
		BaseURL: defaultClaudeAPIURL,
		HTTP:    httpClient,
	}
}

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system"`
	Messages  []claudeMessage `json:"messages"`
}

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Assess sends the gathered CI-failure context to Claude, parses the
// resulting findings array, and — if the first response isn't valid —
// makes one bounded repair attempt before giving up (§7).
func (p *ClaudeProvider) Assess(ctx context.Context, req AssessmentRequest) ([]Assessment, error) {
	prompt := assess.BuildPrompt(req)

	raw, err := p.complete(ctx, assess.SystemPrompt, prompt, 3072)
	if err != nil {
		return nil, fmt.Errorf("claude: assess call failed: %w", err)
	}

	findings, parseErr := assess.ParseAssessments(raw)
	if parseErr == nil {
		assess.ValidateAnchors(req, findings)
		return findings, nil
	}

	repaired, repairErr := p.complete(ctx, assess.RepairSystemPrompt, raw, 2048)
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

// complete is the low-level call shared by the initial assessment and the
// repair attempt. Both are plain single-turn text completions — tools
// stay disabled throughout, per §7.
func (p *ClaudeProvider) complete(ctx context.Context, system, user string, maxTokens int) (string, error) {
	body := claudeRequest{
		Model:     p.Model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  []claudeMessage{{Role: "user", Content: user}},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("x-api-key", p.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

	resp, err := p.HTTP.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var parsed claudeResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("invalid response body: %w", err)
	}
	if resp.StatusCode >= 300 {
		if parsed.Error != nil {
			return "", fmt.Errorf("api error (%d): %s", resp.StatusCode, parsed.Error.Message)
		}
		return "", fmt.Errorf("api error (%d)", resp.StatusCode)
	}
	if len(parsed.Content) == 0 {
		return "", fmt.Errorf("empty response content")
	}
	return parsed.Content[0].Text, nil
}
