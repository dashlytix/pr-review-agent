package provider

import "strings"

// resolveEndpoint normalizes a user-supplied base URL into the full API
// endpoint a provider POSTs to.
//
// Gateways are configured in different shapes in the wild: some docs give
// a bare host ("https://llm.int.exe.xyz"), some an OpenAI-style version
// prefix (".../v1"), some the fully qualified path. Accepting all three
// keeps ANTHROPIC_BASE_URL / OPENAI_BASE_URL copy-pasteable from whatever
// the gateway's own instructions say, instead of silently 404-ing.
//
// path is the provider's canonical suffix, e.g. "/v1/messages".
func resolveEndpoint(base, path string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return path
	}
	// Already fully qualified.
	if strings.HasSuffix(base, path) {
		return base
	}
	// Base carries a version prefix the canonical path repeats
	// (".../v1" + "/v1/messages" must not become ".../v1/v1/messages").
	for _, prefix := range versionPrefixes(path) {
		if strings.HasSuffix(base, prefix) {
			return base + strings.TrimPrefix(path, prefix)
		}
	}
	return base + path
}

// versionPrefixes lists the leading path segments of a canonical endpoint
// path, longest first, so resolveEndpoint can detect and avoid repeating
// any of them ("/v1/messages" -> "/v1").
func versionPrefixes(path string) []string {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	var out []string
	for i := len(segs) - 1; i >= 1; i-- {
		out = append(out, "/"+strings.Join(segs[:i], "/"))
	}
	return out
}
