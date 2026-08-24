package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/dimension/ai-ci-agent/internal/assess"
)

const defaultClaudeAPIURL = "https://api.anthropic.com/v1/messages"

// claudeAPIPath is the canonical Anthropic Messages path appended to any
// base-URL override.
const claudeAPIPath = "/v1/messages"

// defaultClaudeModel is used unless Config.Model overrides it.
const defaultClaudeModel = "claude-sonnet-5"

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
		Model:   defaultClaudeModel,
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

// Text joins every text block in the response, skipping non-text blocks.
//
// Reasoning-capable models (and any model with extended thinking enabled)
// prepend a "thinking" block, so content[0].Text is empty even on a
// perfectly good answer; the API also permits a long answer to arrive
// split across several text blocks. Concatenating just the text blocks
// handles both without caring which model is behind the endpoint.
func (r claudeResponse) Text() string {
	var b strings.Builder
	for _, c := range r.Content {
		if c.Type != "" && c.Type != "text" {
			continue
		}
		b.WriteString(c.Text)
	}
	return strings.TrimSpace(b.String())
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

// Review mirrors Assess but drives the plain PR-review path: a different
// system prompt (assess.ReviewSystemPrompt) and parse function
// (assess.ParseReview), since there's no mandatory "ci-failure" category
// here, and the response carries a summary alongside its findings.
func (p *ClaudeProvider) Review(ctx context.Context, req AssessmentRequest) (ReviewResult, error) {
	prompt := assess.BuildReviewPrompt(req)

	// A large PR's diff+files prompt gives a reasoning-capable model much
	// more to think through than the bounded CI-failure context Assess
	// sends, so this budget is bigger than Assess's 3072 -- too small a
	// budget here means the model can burn it all on the "thinking" block
	// and return no text at all (first observed against a real 1600+ line
	// PR, which is what motivated raising this from 3072 to 8192). Raised
	// again to 16384 after the same "response contained no text blocks"
	// failure recurred against a 386-line PR -- small enough that this
	// isn't purely a diff-size problem; thinking-token usage varies enough
	// on its own that even a moderate diff can exhaust 8192 before any
	// text comes out.
	raw, err := p.complete(ctx, assess.ReviewSystemPrompt, prompt, 16384)
	if err != nil {
		return ReviewResult{}, fmt.Errorf("claude: review call failed: %w", err)
	}

	result, parseErr := assess.ParseReview(raw)
	if parseErr == nil {
		assess.ValidateAnchors(req, result.Findings)
		return result, nil
	}

	repaired, repairErr := p.complete(ctx, assess.ReviewRepairSystemPrompt, raw, 2048)
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
	text := parsed.Text()
	if text == "" {
		// Reasoning models can return only non-text blocks (e.g. a
		// "thinking" block with an empty body when the turn was cut
		// short). Report that rather than handing "" to the parser and,
		// worse, to the repair call — the Messages API rejects an empty
		// user message with a 400, masking the real cause.
		return "", fmt.Errorf("response contained no text blocks")
	}
	return text, nil
}
