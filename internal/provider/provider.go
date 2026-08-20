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
func Get(name, apiKey string) (Provider, error) {
	client := &http.Client{Timeout: 90 * time.Second}
	switch name {
	case "", "claude":
		return NewClaudeProvider(apiKey, client), nil
	case "openai":
		p := NewOpenAIProvider(apiKey, client)
		// OPENAI_BASE_URL/OPENAI_MODEL let this same provider talk to any
		// OpenAI-compatible gateway (e.g. OpenRouter) instead of OpenAI
		// itself, without adding a third provider name to plumb through
		// the llm-provider input.
		if base := os.Getenv("OPENAI_BASE_URL"); base != "" {
			p.BaseURL = base
		}
		if model := os.Getenv("OPENAI_MODEL"); model != "" {
			p.Model = model
		}
		return p, nil
	default:
		return nil, fmt.Errorf("provider: unsupported llm-provider %q", name)
	}
}
