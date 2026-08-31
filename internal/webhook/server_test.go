package webhook

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestServer_StartAndShutdown(t *testing.T) {
	h := &Handler{Secret: testSecret, Client: nil, Provider: nil}
	s := NewServer("127.0.0.1:0", h)

	errCh := make(chan error, 1)
	go func() { errCh <- s.Start() }()

	// Give ListenAndServe a moment to actually bind before shutting down.
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown returned an error: %v", err)
	}

	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			t.Fatalf("Start returned an unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after Shutdown")
	}
}

func TestServer_ShutdownBeforeStartDoesNotHang(t *testing.T) {
	h := &Handler{Secret: testSecret}
	s := NewServer("127.0.0.1:0", h)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown on a never-started server returned an error: %v", err)
	}
}
