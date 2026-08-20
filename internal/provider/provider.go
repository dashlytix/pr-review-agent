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

// AssessmentRequest and Assessment are defined in internal/assess (which
// both concrete providers call into for prompt building and parsing) and
// re-exported here as aliases so callers can write provider.Assessment
// per §4.2 without depending on the internal split.
type AssessmentRequest = assess.AssessmentRequest
type Assessment = assess.Assessment

// Provider is the single interface both ClaudeProvider and OpenAIProvider
// implement, per §4.2. Assess returns a slice rather than a single
// Assessment: exactly one "ci-failure" finding plus zero or more
// additional review findings spotted in the same diff.
type Provider interface {
	Assess(ctx context.Context, req AssessmentRequest) ([]Assessment, error)
}

// Get selects a Provider by name, per the llm-provider action input.
//
// Both providers honour <PROVIDER>_BASE_URL / <PROVIDER>_MODEL overrides so
// either can be pointed at an API-compatible gateway (OpenRouter, or the
// keyless exe.dev LLM gateway) without adding a third provider name to
// plumb through the llm-provider input. resolveEndpoint accepts a bare
// host, a "/v1" prefix, or a fully qualified path for the override.
func Get(name, apiKey string) (Provider, error) {
	client := &http.Client{Timeout: 90 * time.Second}
	switch name {
	case "", "claude":
		p := NewClaudeProvider(apiKey, client)
		if base := os.Getenv("ANTHROPIC_BASE_URL"); base != "" {
			p.BaseURL = resolveEndpoint(base, claudeAPIPath)
		}
		return p, nil
	case "openai":
		p := NewOpenAIProvider(apiKey, client)
		if base := os.Getenv("OPENAI_BASE_URL"); base != "" {
			p.BaseURL = resolveEndpoint(base, openAIAPIPath)
		}
		return p, nil
	default:
		return nil, fmt.Errorf("provider: unsupported llm-provider %q", name)
	}
}
