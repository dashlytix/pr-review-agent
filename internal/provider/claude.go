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

// llm2GatewayURL is the exe-llm gateway ClaudeProvider tries first by
// default (see provider.New) -- reachable only from VMs tagged for
// llm-2 access. Speaks the identical Anthropic Messages wire format, so
// no separate request/response types are needed for it.
const llm2GatewayURL = "https://llm-2.int.exe.xyz/v1/messages"

// llm2GatewayAPIKey is a non-secret sentinel, not a credential: the
// llm-2 gateway authenticates by VM tag at the network edge, so any
// non-empty x-api-key value satisfies the Messages API's required
// header. Safe to keep in source; nothing to rotate or scan for.
const llm2GatewayAPIKey = "implicit"

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

	// FallbackBaseURL/FallbackAPIKey, if both non-empty, are tried when
	// a request against BaseURL fails in a way that implicates BaseURL
	// itself (unreachable, or its credentials/billing are broken)
	// rather than the request's content -- see shouldFallback. Both
	// tiers speak the same Anthropic Messages wire format; this is
	// purely which endpoint/credential gets used, not a second
	// Provider implementation. Left empty, behavior is identical to
	// today's single-endpoint ClaudeProvider.
	FallbackBaseURL string
	FallbackAPIKey  string
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

// complete is the low-level call shared by the initial assessment and the
// repair attempt. Both are plain single-turn text completions — tools
// stay disabled throughout, per §7. Tries BaseURL first, falling
// through to FallbackBaseURL (if configured) on a fallback-eligible
// failure per shouldFallback.
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

	text, err := p.doRequest(ctx, p.BaseURL, p.APIKey, b)
	if err == nil {
		log.Printf("claude: served by primary tier %s", p.BaseURL)
		return text, nil
	}
	if p.FallbackBaseURL == "" || !shouldFallback(err) {
		return "", err
	}

	log.Printf("claude: primary tier %s failed (%v); falling back to %s", p.BaseURL, err, p.FallbackBaseURL)
	text, fbErr := p.doRequest(ctx, p.FallbackBaseURL, p.FallbackAPIKey, b)
	if fbErr != nil {
		return "", fmt.Errorf("primary tier %s failed: %v; fallback tier %s also failed: %w", p.BaseURL, err, p.FallbackBaseURL, fbErr)
	}
	log.Printf("claude: served by fallback tier %s", p.FallbackBaseURL)
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
