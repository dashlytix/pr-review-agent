// Package provider defines the provider-agnostic LLM interface (§4.2) and
// the concrete implementations selected at runtime via the llm-provider
// input. Every implementation must return the same Assessment shape —
// "provider parity" per §6.1 — so switching providers is a configuration
// change, not a code change.
package provider

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/dimension/ai-ci-agent/internal/assess"
)

// AssessmentRequest, Assessment, and ReviewResult are defined in
// internal/assess (which both concrete providers call into for prompt
// building and parsing) and re-exported here as aliases so callers can
// write provider.Assessment per §4.2 without depending on the internal
// split.
type AssessmentRequest = assess.AssessmentRequest
type Assessment = assess.Assessment
type ReviewResult = assess.ReviewResult

// Provider is the single interface both ClaudeProvider and OpenAIProvider
// implement, per §4.2. Assess returns a slice rather than a single
// Assessment: exactly one "ci-failure" finding plus zero or more
// additional review findings spotted in the same diff.
//
// Review drives the separate plain-PR-review path (the pull_request
// opened/synchronize trigger, independent of any CI outcome): no finding
// category is mandatory, and an empty Findings slice is the common "no
// issues found" result. ReviewResult.Summary is generated in the same
// call as Findings, not a second one.
//
// Answer drives the Slack Q&A path (internal/slackbot): a free-form
// question about the same PR-diff context Review uses, answered as
// plain text rather than a structured finding — no JSON contract, no
// parse/repair step, just the model's raw response.
type Provider interface {
	Assess(ctx context.Context, req AssessmentRequest) ([]Assessment, error)
	Review(ctx context.Context, req AssessmentRequest) (ReviewResult, error)
	Answer(ctx context.Context, req AssessmentRequest, question string) (string, error)
}

// envOr reads an environment variable, falling back to def when unset or
// empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Config is the resolved provider configuration for one run. It exists
// so the base URL and model can arrive as explicit action inputs
// (llm-base-url / llm-model) rather than only as ambient environment
// variables: the Action's inputs are its documented contract, and a
// gateway endpoint is configuration a workflow author should be able to
// set in the same place as the provider name.
type Config struct {
	// Name is the llm-provider input: "" or "claude", or "openai".
	Name string
	// APIKey is the llm-api-key input. Gateways that inject credentials
	// at the network edge still need a non-empty placeholder.
	APIKey string
	// BaseURL optionally redirects the provider at an API-compatible
	// gateway. It may be a bare host, a "/v1" prefix, or a fully
	// qualified endpoint — see resolveEndpoint.
	BaseURL string
	// Model optionally overrides the provider's default model.
	Model string
	// ProxyAPIKey is the key for the default llm-proxy primary tier
	// (see New) — a real credential, distinct from APIKey, which that
	// same default chain uses for its direct-Anthropic fallback tier.
	// Only consulted when Name is "" or "claude" and BaseURL is unset.
	ProxyAPIKey string
	// FallbackModel overrides the model sent to the default chain's
	// direct-Anthropic fallback tier. Empty uses the built-in
	// defaultClaudeModel. Model, by contrast, now names the primary
	// (llm-proxy GPT) tier's model — the two no longer share one value
	// now that the tiers can be different model families.
	FallbackModel string
}

// New builds a Provider from an explicit Config, with no environment
// access of its own — callers resolve inputs first (see ConfigFromEnv),
// which keeps configuration precedence in one visible place instead of
// spread across constructors.
func New(cfg Config) (Provider, error) {
	// 180s, not 90s: Review's larger completion budget (see claude.go) can
	// take longer to generate than this client waited for, observed as
	// "Client.Timeout exceeded while awaiting headers" against a large
	// real PR. Still comfortably under the 4-minute default step timeout
	// (stepTimeout in cmd/agent/main.go), leaving room for the rest of
	// the gather/post pipeline.
	client := &http.Client{Timeout: 180 * time.Second}
	switch cfg.Name {
	case "", "claude":
		p := NewClaudeProvider(cfg.APIKey, client)
		if cfg.BaseURL == "" {
			// No explicit gateway named (action input or env var):
			// default to the two-tier chain -- try the llm-proxy GPT
			// model first (OpenAI wire format, real credential via
			// ProxyAPIKey), falling back to calling Anthropic directly
			// with the real configured key on a tier-1-specific
			// failure. An explicit BaseURL means a workflow author
			// named their own gateway; that shouldn't be silently
			// supplemented with a fallback they didn't ask for,
			// matching this repo's existing "inputs win over ambient
			// config" precedent.
			if cfg.ProxyAPIKey == "" {
				return nil, fmt.Errorf("provider: llm-proxy-api-key is required for the default claude two-tier chain (no llm-base-url override was given)")
			}
			if cfg.Model == "" {
				return nil, fmt.Errorf("provider: llm-model is required for the default claude two-tier chain (no safe default for the llm-proxy GPT model)")
			}
			p.BaseURL = resolveEndpoint(llmProxyURL, openAIAPIPath)
			p.APIKey = cfg.ProxyAPIKey
			p.PrimaryOpenAIStyle = true
			p.Model = cfg.Model
			p.FallbackBaseURL = defaultClaudeAPIURL
			p.FallbackAPIKey = cfg.APIKey
			p.FallbackModel = cfg.FallbackModel
			if p.FallbackModel == "" {
				p.FallbackModel = defaultClaudeModel
			}
			return p, nil
		}
		p.BaseURL = resolveEndpoint(cfg.BaseURL, claudeAPIPath)
		if cfg.Model != "" {
			p.Model = cfg.Model
		}
		return p, nil
	case "openai":
		p := NewOpenAIProvider(cfg.APIKey, client)
		if cfg.BaseURL != "" {
			p.BaseURL = resolveEndpoint(cfg.BaseURL, openAIAPIPath)
		}
		if cfg.Model != "" {
			p.Model = cfg.Model
		}
		return p, nil
	default:
		return nil, fmt.Errorf("provider: unsupported llm-provider %q", cfg.Name)
	}
}

// ConfigFromEnv resolves the base URL and model for a provider from the
// environment, so the eval harness and any plain-shell invocation keep
// working without action inputs.
//
// Precedence, highest first: the provider-specific variable
// (ANTHROPIC_BASE_URL / OPENAI_BASE_URL), then the provider-neutral
// LLM_BASE_URL / LLM_MODEL. The neutral pair means a single pair of
// variables can point either provider at the same gateway — useful when
// one endpoint serves both API shapes.
func ConfigFromEnv(name, apiKey string) Config {
	cfg := Config{Name: name, APIKey: apiKey}
	switch name {
	case "", "claude":
		cfg.BaseURL = envOr("ANTHROPIC_BASE_URL", os.Getenv("LLM_BASE_URL"))
		cfg.Model = envOr("ANTHROPIC_MODEL", os.Getenv("LLM_MODEL"))
		cfg.ProxyAPIKey = os.Getenv("LLM_PROXY_API_KEY")
		cfg.FallbackModel = os.Getenv("ANTHROPIC_FALLBACK_MODEL")
	case "openai":
		cfg.BaseURL = envOr("OPENAI_BASE_URL", os.Getenv("LLM_BASE_URL"))
		cfg.Model = envOr("OPENAI_MODEL", os.Getenv("LLM_MODEL"))
	}
	return cfg
}

// Get selects a Provider by name, taking base URL and model from the
// environment. It's the convenience path for callers with no explicit
// configuration to pass (the eval harness); the Action itself uses New
// with inputs resolved from action.yml.
func Get(name, apiKey string) (Provider, error) {
	return New(ConfigFromEnv(name, apiKey))
}
