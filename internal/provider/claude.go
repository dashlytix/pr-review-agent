package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/dimension/ai-ci-agent/internal/assess"
)

const defaultClaudeAPIURL = "https://api.anthropic.com/v1/messages"

// llmProxyURL is the GPT-model gateway ClaudeProvider tries first by
// default (see provider.New) -- an OpenAI-compatible endpoint, so
// requests to it go out via PrimaryOpenAIStyle instead of the Anthropic
// Messages format. Unlike the old llm-2 gateway this requires a real
// credential (Config.ProxyAPIKey), not a VM-tag-scoped sentinel.
const llmProxyURL = "https://llm-proxy.int.exe.xyz"

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

	// PrimaryOpenAIStyle, when true, sends BaseURL requests in the
	// OpenAI chat-completions wire format instead of the Anthropic
	// Messages format. This lets the primary tier be a GPT model behind
	// an OpenAI-compatible gateway (e.g. the default llm-proxy tier —
	// see provider.New) while FallbackBaseURL still speaks straight to
	// Anthropic. Defaults false, matching every existing single-format
	// ClaudeProvider.
	PrimaryOpenAIStyle bool

	// FallbackBaseURL/FallbackAPIKey, if both non-empty, are tried when
	// a request against BaseURL fails in a way that implicates BaseURL
	// itself (unreachable, or its credentials/billing are broken)
	// rather than the request's content -- see shouldFallback. The
	// fallback tier always speaks the Anthropic Messages format
	// (regardless of PrimaryOpenAIStyle), since it exists to reach
	// Claude directly. Left empty, behavior is identical to today's
	// single-endpoint ClaudeProvider.
	FallbackBaseURL string
	FallbackAPIKey  string
	// FallbackModel overrides the model sent to FallbackBaseURL. Empty
	// falls back to Model, preserving single-tier behavior when only
	// one model was ever configured.
	FallbackModel string
}

func NewClaudeProvider(apiKey string, httpClient *http.Client) *ClaudeProvider {
	return &ClaudeProvider{
		APIKey:  apiKey,
		Model:   defaultClaudeModel,
		BaseURL: defaultClaudeAPIURL,
		HTTP:    httpClient,
	}
}

// apiStatusError carries the real HTTP status code from a non-2xx
// response, independent of whether the body happened to be valid JSON.
// A gateway's plain-text error page (e.g. exe-llm's 402 "LLM credits
// exhausted") must still surface its status code -- checking this
// before attempting json.Unmarshal is what makes that possible.
type apiStatusError struct {
	StatusCode int
	Message    string
}

func (e *apiStatusError) Error() string {
	return fmt.Sprintf("api error (%d): %s", e.StatusCode, e.Message)
}

// transportError means no HTTP response was ever received for this
// tier: connection refused, DNS failure, TLS failure, or a timeout
// (context deadline or the http.Client's own Timeout).
type transportError struct{ err error }

func (e *transportError) Error() string { return fmt.Sprintf("request failed: %v", e.err) }
func (e *transportError) Unwrap() error { return e.err }

// shouldFallback reports whether err implicates the tier itself --
// unreachable, or misconfigured/exhausted credentials -- rather than
// the request's content. Only the former is worth retrying against a
// second tier; a content-level failure (malformed prompt, context
// length exceeded) would fail identically there, wasting a call and
// masking the real cause.
func shouldFallback(err error) bool {
	var statusErr *apiStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.StatusCode {
		case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden:
			return true
		}
		return false
	}
	var transportErr *transportError
	return errors.As(err, &transportErr)
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

// answerMaxTokens bounds a Q&A reply: short enough for a Slack message,
// well under Review's 16384 (there's no findings schema to fill in here,
// just a few sentences of prose).
const answerMaxTokens = 1024

// Answer drives the Slack Q&A path: a free-form question about the same
// diff/files context Review uses, answered as plain text. Unlike
// Assess/Review there's no JSON contract to parse, so a successful call
// returns the model's raw text directly -- no repair-retry step.
func (p *ClaudeProvider) Answer(ctx context.Context, req AssessmentRequest, question string) (string, error) {
	prompt := assess.BuildAnswerPrompt(req, question)

	text, err := p.complete(ctx, assess.AnswerSystemPrompt, prompt, answerMaxTokens)
	if err != nil {
		return "", fmt.Errorf("claude: answer call failed: %w", err)
	}
	return text, nil
}

// complete is the low-level call shared by the initial assessment and the
// repair attempt. Both are plain single-turn text completions — tools
// stay disabled throughout, per §7. Tries BaseURL first (in whichever
// wire format PrimaryOpenAIStyle selects), falling through to
// FallbackBaseURL (always Anthropic-format, if configured) on a
// fallback-eligible failure per shouldFallback.
func (p *ClaudeProvider) complete(ctx context.Context, system, user string, maxTokens int) (string, error) {
	text, err := p.doPrimary(ctx, system, user, maxTokens)
	if err == nil {
		log.Printf("claude: served by primary tier %s", p.BaseURL)
		return text, nil
	}
	if p.FallbackBaseURL == "" || !shouldFallback(err) {
		return "", err
	}

	log.Printf("claude: primary tier %s failed (%v); falling back to %s", p.BaseURL, err, p.FallbackBaseURL)
	text, fbErr := p.doFallback(ctx, system, user, maxTokens)
	if fbErr != nil {
		return "", fmt.Errorf("primary tier %s failed: %v; fallback tier %s also failed: %w", p.BaseURL, err, p.FallbackBaseURL, fbErr)
	}
	log.Printf("claude: served by fallback tier %s", p.FallbackBaseURL)
	return text, nil
}

// doPrimary sends the request to BaseURL, in Anthropic format unless
// PrimaryOpenAIStyle selects the OpenAI chat-completions format.
func (p *ClaudeProvider) doPrimary(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if p.PrimaryOpenAIStyle {
		return p.doOpenAIRequest(ctx, p.BaseURL, p.APIKey, p.Model, system, user, maxTokens)
	}
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
	return p.doRequest(ctx, p.BaseURL, p.APIKey, b)
}

// doFallback sends the request to FallbackBaseURL, always in Anthropic
// format — the fallback tier exists specifically to reach Claude
// directly, independent of what wire format the primary tier used.
func (p *ClaudeProvider) doFallback(ctx context.Context, system, user string, maxTokens int) (string, error) {
	model := p.FallbackModel
	if model == "" {
		model = p.Model
	}
	body := claudeRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  []claudeMessage{{Role: "user", Content: user}},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	return p.doRequest(ctx, p.FallbackBaseURL, p.FallbackAPIKey, b)
}

// proxyChatRequest is the OpenAI chat-completions request body sent to
// an OpenAI-compatible primary tier (see PrimaryOpenAIStyle). It reuses
// openAIMessage/openAIResponse from openai.go (same package) and adds
// MaxTokens, which OpenAIProvider itself never sets.
type proxyChatRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	MaxTokens int             `json:"max_tokens,omitempty"`
}

// doOpenAIRequest sends one chat-completions request in the OpenAI wire
// format and returns its decoded text. Mirrors doRequest's error
// classification (apiStatusError/transportError) so shouldFallback
// applies identically regardless of which format the primary tier used.
func (p *ClaudeProvider) doOpenAIRequest(ctx context.Context, baseURL, apiKey, model, system, user string, maxTokens int) (string, error) {
	body := proxyChatRequest{
		Model: model,
		Messages: []openAIMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		MaxTokens: maxTokens,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.HTTP.Do(httpReq)
	if err != nil {
		return "", &transportError{err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		var parsed openAIResponse
		if json.Unmarshal(raw, &parsed) == nil && parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Message
		}
		return "", &apiStatusError{StatusCode: resp.StatusCode, Message: msg}
	}

	var parsed openAIResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("invalid response body: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("empty response choices")
	}
	text := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("response contained no text content")
	}
	return text, nil
}

// doRequest sends one already-marshaled claudeRequest body to a single
// tier's endpoint/credential pair and returns its decoded text.
func (p *ClaudeProvider) doRequest(ctx context.Context, baseURL, apiKey string, body []byte) (string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("x-api-key", apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("content-type", "application/json")

	resp, err := p.HTTP.Do(httpReq)
	if err != nil {
		return "", &transportError{err: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	// Status is checked before attempting to decode as JSON: a gateway
	// error page (e.g. exe-llm's plain-text 402) isn't valid JSON, and
	// decoding first would swallow the real status code inside a
	// confusing "invalid character ... looking for beginning of value"
	// error -- exactly what made an earlier 402 look like a decode bug.
	if resp.StatusCode >= 300 {
		msg := strings.TrimSpace(string(raw))
		var parsed claudeResponse
		if json.Unmarshal(raw, &parsed) == nil && parsed.Error != nil && parsed.Error.Message != "" {
			msg = parsed.Error.Message
		}
		return "", &apiStatusError{StatusCode: resp.StatusCode, Message: msg}
	}

	var parsed claudeResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("invalid response body: %w", err)
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
