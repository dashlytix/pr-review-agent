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

func TestSend_PostsJSONAttachmentBody(t *testing.T) {
	var gotContentType string
	var gotBody SlackAttachmentMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if err := Send(context.Background(), server.URL, testMessage()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if len(gotBody.Attachments) != 1 || gotBody.Attachments[0].Color != colorOpened {
		t.Errorf("posted body = %+v, want the rendered attachment with color %q", gotBody, colorOpened)
	}
}

func TestSend_NonOKStatusIsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("invalid_payload"))
	}))
	defer server.Close()

	err := Send(context.Background(), server.URL, testMessage())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestSend_EmptyWebhookURLIsNoop(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
	}))
	defer server.Close()

	if err := Send(context.Background(), "", testMessage()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 0 {
		t.Errorf("expected no requests with an empty webhook URL, got %d", calls)
	}
}
