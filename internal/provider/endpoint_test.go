package provider

import (
	"os"
	"testing"
)

func TestResolveEndpoint(t *testing.T) {
	cases := []struct {
		name string
		base string
		path string
		want string
	}{
		{"empty base keeps canonical path", "", "/v1/messages", "/v1/messages"},
		{"bare host", "https://llm.example", "/v1/messages", "https://llm.example/v1/messages"},
		{"bare host trailing slash", "https://llm.example/", "/v1/messages", "https://llm.example/v1/messages"},
		{"version prefix not repeated", "https://llm.example/v1", "/v1/messages", "https://llm.example/v1/messages"},
		{"fully qualified left alone", "https://llm.example/v1/messages", "/v1/messages", "https://llm.example/v1/messages"},
		{"openai nested path prefix", "https://gw.example/api/v1", "/v1/chat/completions", "https://gw.example/api/v1/chat/completions"},
		{"openai fully qualified", "https://gw.example/api/v1/chat/completions", "/v1/chat/completions", "https://gw.example/api/v1/chat/completions"},
		{"surrounding whitespace trimmed", "  https://llm.example  ", "/v1/messages", "https://llm.example/v1/messages"},
	}
	for _, c := range cases {
		if got := resolveEndpoint(c.base, c.path); got != c.want {
			t.Errorf("%s: resolveEndpoint(%q, %q) = %q, want %q", c.name, c.base, c.path, got, c.want)
		}
	}
}

func TestGet_ClaudeRespectsEnvOverrides(t *testing.T) {
	t.Setenv("ANTHROPIC_BASE_URL", "https://llm.int.exe.xyz")
	t.Setenv("ANTHROPIC_MODEL", "claude-opus-5")

	p, err := Get("claude", "implicit")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cp, ok := p.(*ClaudeProvider)
	if !ok {
		t.Fatalf("Get(\"claude\") = %T, want *ClaudeProvider", p)
	}
	if want := "https://llm.int.exe.xyz/v1/messages"; cp.BaseURL != want {
		t.Errorf("BaseURL = %q, want %q", cp.BaseURL, want)
	}
	if cp.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want the ANTHROPIC_MODEL override", cp.Model)
	}
}

func TestGet_ClaudeWithoutEnvOverridesUsesDefaults(t *testing.T) {
	os.Unsetenv("ANTHROPIC_BASE_URL")
	os.Unsetenv("ANTHROPIC_MODEL")

	p, err := Get("claude", "key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cp := p.(*ClaudeProvider)
	if cp.BaseURL != defaultClaudeAPIURL {
		t.Errorf("BaseURL = %q, want the Anthropic default", cp.BaseURL)
	}
	if cp.Model != "claude-sonnet-5" {
		t.Errorf("Model = %q, want the built-in default", cp.Model)
	}
}

func TestNew_AppliesExplicitConfig(t *testing.T) {
	// Config must win outright — no environment consulted by New, so an
	// ambient variable on a CI runner can't quietly redirect a workflow
	// that spelled the gateway out in its inputs.
	t.Setenv("ANTHROPIC_BASE_URL", "https://ambient.example")
	t.Setenv("ANTHROPIC_MODEL", "ambient-model")

	p, err := New(Config{Name: "claude", APIKey: "k", BaseURL: "https://gw.example", Model: "claude-opus-5"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cp := p.(*ClaudeProvider)
	if want := "https://gw.example/v1/messages"; cp.BaseURL != want {
		t.Errorf("BaseURL = %q, want %q", cp.BaseURL, want)
	}
	if cp.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want the Config value", cp.Model)
	}
}

func TestNew_EmptyConfigUsesProviderDefaults(t *testing.T) {
	p, err := New(Config{Name: "openai", APIKey: "k"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	op := p.(*OpenAIProvider)
	if op.BaseURL != defaultOpenAIAPIURL {
		t.Errorf("BaseURL = %q, want the OpenAI default", op.BaseURL)
	}
	if op.Model != defaultOpenAIModel {
		t.Errorf("Model = %q, want the built-in default", op.Model)
	}
}

func TestNew_UnsupportedProviderErrors(t *testing.T) {
	if _, err := New(Config{Name: "bogus"}); err == nil {
		t.Fatal("expected an error for an unsupported provider name")
	}
}

// The provider-neutral LLM_BASE_URL/LLM_MODEL pair lets one gateway that
// serves both API shapes be configured once, whichever provider is used.
func TestConfigFromEnv_NeutralVarsApplyToEitherProvider(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "https://llm.int.exe.xyz")
	t.Setenv("LLM_MODEL", "shared-model")
	os.Unsetenv("ANTHROPIC_BASE_URL")
	os.Unsetenv("ANTHROPIC_MODEL")
	os.Unsetenv("OPENAI_BASE_URL")
	os.Unsetenv("OPENAI_MODEL")

	for _, name := range []string{"claude", "openai"} {
		cfg := ConfigFromEnv(name, "k")
		if cfg.BaseURL != "https://llm.int.exe.xyz" {
			t.Errorf("%s: BaseURL = %q, want the LLM_BASE_URL value", name, cfg.BaseURL)
		}
		if cfg.Model != "shared-model" {
			t.Errorf("%s: Model = %q, want the LLM_MODEL value", name, cfg.Model)
		}
	}
}

func TestConfigFromEnv_ProviderSpecificVarsBeatNeutralOnes(t *testing.T) {
	t.Setenv("LLM_BASE_URL", "https://neutral.example")
	t.Setenv("LLM_MODEL", "neutral-model")
	t.Setenv("ANTHROPIC_BASE_URL", "https://anthropic.example")
	t.Setenv("ANTHROPIC_MODEL", "claude-opus-5")

	cfg := ConfigFromEnv("claude", "k")
	if cfg.BaseURL != "https://anthropic.example" {
		t.Errorf("BaseURL = %q, want the provider-specific value", cfg.BaseURL)
	}
	if cfg.Model != "claude-opus-5" {
		t.Errorf("Model = %q, want the provider-specific value", cfg.Model)
	}
}
