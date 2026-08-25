package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testMessage() SlackAttachmentMessage {
	return RenderOpened(PullRequest{Number: 1, Repo: "widgets", HTMLURL: "https://github.com/o/r/pull/1", Author: "alice"})
}

// withStubSlackAPI points chatPostMessageURL at a local test server for
// the duration of the test, restoring the real endpoint after.
func withStubSlackAPI(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	original := chatPostMessageURL
	chatPostMessageURL = server.URL
	t.Cleanup(func() { chatPostMessageURL = original })
}

func TestPost_PostsJSONAttachmentBodyAndReturnsTS(t *testing.T) {
	var gotContentType, gotAuth string
	var gotBody chatPostMessageRequest
	withStubSlackAPI(t, func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(chatPostMessageResponse{OK: true, TS: "1700000000.000100"})
	})

	cfg := SlackConfig{BotToken: "xoxb-test", Channel: "C123"}
	ts, err := Post(context.Background(), cfg, testMessage(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ts != "1700000000.000100" {
		t.Errorf("ts = %q, want the ts chat.postMessage returned", ts)
	}
	if gotContentType != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", gotContentType)
	}
	if gotAuth != "Bearer xoxb-test" {
		t.Errorf("Authorization = %q, want bot token bearer header", gotAuth)
	}
	if gotBody.Channel != "C123" {
		t.Errorf("channel = %q, want %q", gotBody.Channel, "C123")
	}
	if gotBody.ThreadTS != "" {
		t.Errorf("thread_ts = %q, want empty for a new top-level message", gotBody.ThreadTS)
	}
	if len(gotBody.Attachments) != 1 || gotBody.Attachments[0].Color != colorOpened {
		t.Errorf("posted body = %+v, want the rendered attachment with color %q", gotBody, colorOpened)
	}
}

func TestPost_ThreadTSIsForwardedAsReply(t *testing.T) {
	var gotBody chatPostMessageRequest
	withStubSlackAPI(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		json.NewEncoder(w).Encode(chatPostMessageResponse{OK: true, TS: "1700000000.000200"})
	})

	cfg := SlackConfig{BotToken: "xoxb-test", Channel: "C123"}
	if _, err := Post(context.Background(), cfg, testMessage(), "1700000000.000100"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotBody.ThreadTS != "1700000000.000100" {
		t.Errorf("thread_ts = %q, want the parent ts passed through as a reply", gotBody.ThreadTS)
	}
}

func TestPost_SlackAPIErrorIsError(t *testing.T) {
	withStubSlackAPI(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(chatPostMessageResponse{OK: false, Error: "channel_not_found"})
	})

	cfg := SlackConfig{BotToken: "xoxb-test", Channel: "C123"}
	_, err := Post(context.Background(), cfg, testMessage(), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPost_NonOKStatusIsError(t *testing.T) {
	withStubSlackAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	cfg := SlackConfig{BotToken: "xoxb-test", Channel: "C123"}
	_, err := Post(context.Background(), cfg, testMessage(), "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestPost_DisabledConfigIsNoop(t *testing.T) {
	var calls int
	withStubSlackAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
	})

	for _, cfg := range []SlackConfig{{}, {BotToken: "xoxb-test"}, {Channel: "C123"}} {
		ts, err := Post(context.Background(), cfg, testMessage(), "")
		if err != nil {
			t.Fatalf("unexpected error for %+v: %v", cfg, err)
		}
		if ts != "" {
			t.Errorf("ts = %q for disabled cfg %+v, want empty", ts, cfg)
		}
	}
	if calls != 0 {
		t.Errorf("expected no requests with a disabled config, got %d", calls)
	}
}
