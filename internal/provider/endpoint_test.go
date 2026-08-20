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
