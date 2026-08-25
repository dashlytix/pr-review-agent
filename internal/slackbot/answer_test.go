package slackbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dimension/ai-ci-agent/internal/ghclient"
	"github.com/dimension/ai-ci-agent/internal/provider"
)

func TestStripMention(t *testing.T) {
	tests := []struct{ in, want string }{
		{"<@U0123ABC> why did this fail?", "why did this fail?"},
		{"<@U0123ABC>why did this fail?", "why did this fail?"},
		{"no mention here", "no mention here"},
		{"<@U0123ABC>   ", ""},
	}
	for _, tt := range tests {
		if got := stripMention(tt.in); got != tt.want {
			t.Errorf("stripMention(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestIsThreadReply(t *testing.T) {
	tests := []struct {
		name string
		m    Mention
		want bool
	}{
		{"genuine reply", Mention{TS: "2", ThreadTS: "1"}, true},
		{"no thread at all", Mention{TS: "1", ThreadTS: ""}, false},
		{"root message mentioning itself", Mention{TS: "1", ThreadTS: "1"}, false},
	}
	for _, tt := range tests {
		if got := isThreadReply(tt.m); got != tt.want {
			t.Errorf("%s: isThreadReply(%+v) = %v, want %v", tt.name, tt.m, got, tt.want)
		}
	}
}

// fakeProvider is a minimal provider.Provider for tests -- only Answer
// is exercised by this package, so Assess/Review just fail loudly if
// ever called by mistake.
type fakeProvider struct {
	answerFn func(ctx context.Context, req provider.AssessmentRequest, question string) (string, error)
	// gotReq/gotQuestion record the last Answer call's arguments, for
	// tests that want to assert on what handleMention actually sent.
	gotReq      provider.AssessmentRequest
	gotQuestion string
}

func (f *fakeProvider) Assess(ctx context.Context, req provider.AssessmentRequest) ([]provider.Assessment, error) {
	panic("Assess should not be called by the Slack Q&A path")
}
func (f *fakeProvider) Review(ctx context.Context, req provider.AssessmentRequest) (provider.ReviewResult, error) {
	panic("Review should not be called by the Slack Q&A path")
}
func (f *fakeProvider) Answer(ctx context.Context, req provider.AssessmentRequest, question string) (string, error) {
	f.gotReq = req
	f.gotQuestion = question
	if f.answerFn != nil {
		return f.answerFn(ctx, req, question)
	}
	return "a canned answer", nil
}

func testGHClient(t *testing.T, handler http.HandlerFunc) *ghclient.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := ghclient.New("test-token", "owner", "repo")
	c.BaseURL = server.URL
	c.RetryBaseDelay = time.Millisecond
	return c
}

type issueComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

type pullRequestSummary struct {
	Number int `json:"number"`
}

// githubStub serves the small fixed set of endpoints handleMention's
// pipeline touches: listing open PRs, one PR's marker comment (for the
// reverse lookup), and its diff/files (for gather.GatherForReview).
func githubStub(t *testing.T, prNumber int, threadTS, diff string) *ghclient.Client {
	t.Helper()
	return testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]pullRequestSummary{{Number: prNumber}})
		case r.URL.Path == "/repos/owner/repo/issues/"+itoa(prNumber)+"/comments":
			json.NewEncoder(w).Encode([]issueComment{{ID: 1, Body: "<!-- ai-ci-agent:slack-thread:ts=" + threadTS + " -->"}})
		case r.URL.Path == "/repos/owner/repo/pulls/"+itoa(prNumber):
			w.Write([]byte(diff))
		case r.URL.Path == "/repos/owner/repo/pulls/"+itoa(prNumber)+"/files":
			json.NewEncoder(w).Encode([]map[string]string{{"filename": "a.go", "patch": "+ removed nil check"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func TestHandleMention_Success_CallsProviderWithDiffAndQuestion(t *testing.T) {
	client := githubStub(t, 42, "1700000000.000100", "diff --git a/a.go b/a.go\n+ removed nil check")
	fp := &fakeProvider{answerFn: func(ctx context.Context, req provider.AssessmentRequest, question string) (string, error) {
		return "It fails because the nil check was removed.", nil
	}}
	cfg := Config{Client: client, Provider: fp} // BotToken/Channel empty -- notify.Post becomes a no-op, no real Slack call

	m := Mention{Channel: "C123", TS: "1700000000.000200", ThreadTS: "1700000000.000100", Text: "<@BOT> why did this fail?"}
	handleMention(context.Background(), cfg, newPRCache(client), m)

	if fp.gotQuestion != "why did this fail?" {
		t.Errorf("question sent to provider = %q, want the mention stripped of its <@BOT> prefix", fp.gotQuestion)
	}
	if fp.gotReq.Diff == "" {
		t.Error("expected the PR's diff to be passed to Provider.Answer")
	}
}

func TestHandleMention_NotAThreadReply_NeverCallsGitHubOrProvider(t *testing.T) {
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected GitHub call for a non-thread mention: %s", r.URL.Path)
	})
	fp := &fakeProvider{answerFn: func(ctx context.Context, req provider.AssessmentRequest, question string) (string, error) {
		t.Fatal("Provider.Answer should not be called for a non-thread mention")
		return "", nil
	}}
	cfg := Config{Client: client, Provider: fp}

	for _, m := range []Mention{
		{TS: "1", ThreadTS: "", Text: "<@BOT> hello"},
		{TS: "1", ThreadTS: "1", Text: "<@BOT> hello"}, // the root message mentioning itself
	} {
		handleMention(context.Background(), cfg, newPRCache(client), m)
	}
}

func TestHandleMention_WrongChannel_NeverCallsGitHubOrProvider(t *testing.T) {
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected GitHub call for a wrong-channel mention: %s", r.URL.Path)
	})
	fp := &fakeProvider{answerFn: func(ctx context.Context, req provider.AssessmentRequest, question string) (string, error) {
		t.Fatal("Provider.Answer should not be called for a wrong-channel mention")
		return "", nil
	}}
	cfg := Config{Client: client, Provider: fp, Channel: "C-scoped"}

	m := Mention{Channel: "C-other", TS: "2", ThreadTS: "1", Text: "<@BOT> hello"}
	handleMention(context.Background(), cfg, newPRCache(client), m)
}

func TestHandleMention_NoOpenPRForThread_NeverCallsProvider(t *testing.T) {
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo/pulls":
			json.NewEncoder(w).Encode([]pullRequestSummary{}) // no open PRs at all
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	})
	fp := &fakeProvider{answerFn: func(ctx context.Context, req provider.AssessmentRequest, question string) (string, error) {
		t.Fatal("Provider.Answer should not be called when no PR is found for the thread")
		return "", nil
	}}
	cfg := Config{Client: client, Provider: fp}

	m := Mention{Channel: "C123", TS: "2", ThreadTS: "no-such-thread", Text: "<@BOT> why?"}
	handleMention(context.Background(), cfg, newPRCache(client), m)
}

func TestHandleMention_EmptyQuestionAfterStrip_NeverCallsGitHub(t *testing.T) {
	client := testGHClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected GitHub call for an empty question: %s", r.URL.Path)
	})
	fp := &fakeProvider{}
	cfg := Config{Client: client, Provider: fp}

	m := Mention{Channel: "C123", TS: "2", ThreadTS: "1", Text: "<@BOT>   "}
	handleMention(context.Background(), cfg, newPRCache(client), m)
}
