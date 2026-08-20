package provider

import (
	"os"
	"testing"
)

func TestGet_DefaultsToClaudeProvider(t *testing.T) {
	for _, name := range []string{"", "claude"} {
		p, err := Get(name, "key")
		if err != nil {
			t.Fatalf("Get(%q): unexpected error: %v", name, err)
		}
		if _, ok := p.(*ClaudeProvider); !ok {
			t.Errorf("Get(%q) = %T, want *ClaudeProvider", name, p)
		}
	}
}

func TestGet_OpenAI(t *testing.T) {
	p, err := Get("openai", "key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := p.(*OpenAIProvider); !ok {
		t.Errorf("Get(\"openai\") = %T, want *OpenAIProvider", p)
	}
}

func TestGet_UnsupportedProviderErrors(t *testing.T) {
	if _, err := Get("bogus", "key"); err == nil {
		t.Fatal("expected an error for an unsupported provider name")
	}
}

func TestGet_OpenAIRespectsEnvOverrides(t *testing.T) {
	os.Setenv("OPENAI_BASE_URL", "https://openrouter.ai/api/v1/chat/completions")
	os.Setenv("OPENAI_MODEL", "openai/gpt-4o-mini")
	defer os.Unsetenv("OPENAI_BASE_URL")
	defer os.Unsetenv("OPENAI_MODEL")

	p, err := Get("openai", "key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	oa, ok := p.(*OpenAIProvider)
	if !ok {
		t.Fatalf("Get(\"openai\") = %T, want *OpenAIProvider", p)
	}
	if oa.BaseURL != "https://openrouter.ai/api/v1/chat/completions" {
		t.Errorf("BaseURL = %q, want the OPENAI_BASE_URL override", oa.BaseURL)
	}
	if oa.Model != "openai/gpt-4o-mini" {
		t.Errorf("Model = %q, want the OPENAI_MODEL override", oa.Model)
	}
}

func TestGet_OpenAIWithoutEnvOverridesUsesDefaults(t *testing.T) {
	os.Unsetenv("OPENAI_BASE_URL")
	os.Unsetenv("OPENAI_MODEL")

	p, err := Get("openai", "key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	oa := p.(*OpenAIProvider)
	if oa.BaseURL != defaultOpenAIAPIURL {
		t.Errorf("BaseURL = %q, want the default %q", oa.BaseURL, defaultOpenAIAPIURL)
	}
}
