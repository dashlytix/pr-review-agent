package webhook

import (
	"context"
	"net"
	"net/http"
	"time"
)

// shutdownGracePeriod bounds how long Shutdown waits for in-flight
// *HTTP* requests to finish before giving up. It does not wait for the
// detached review-pipeline goroutines Handler.ServeHTTP starts (see its
// doc comment) -- those already run past any single HTTP request's
// lifetime by design, matching how cmd/slackbot's own mention-handling
// goroutines aren't waited on during its shutdown either (see
// internal/slackbot/daemon.go).
const shutdownGracePeriod = 10 * time.Second

// Server wraps http.Server with this package's routing (a single
// POST /webhooks/github route) and a Start/Shutdown pair that mirrors
// cmd/slackbot's signal.NotifyContext-driven shutdown shape, so
// cmd/webhookserver's main() reads the same as cmd/slackbot's.
type Server struct {
	httpServer *http.Server
}

// NewServer builds a Server bound to addr, routing POST /webhooks/github
// to h. Path is fixed rather than configurable -- this repo has exactly
// one webhook source (GitHub) and one endpoint to model it, and a fixed
// path is one less thing to misconfigure between the GitHub App's
// webhook URL and this server.
func NewServer(addr string, h *Handler) *Server {
	mux := http.NewServeMux()
	mux.Handle("/webhooks/github", h)

	return &Server{
		httpServer: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
	}
}

// Start blocks serving HTTP until the server is shut down (via
// Shutdown) or fails to start, mirroring http.Server.ListenAndServe's
// contract: a clean shutdown returns http.ErrServerClosed, which callers
// should treat as success, not failure (see cmd/webhookserver/main.go).
func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

// Serve is Start's counterpart for a caller that already has a bound
// net.Listener -- e.g. a test that needs to know the ephemeral port
// before the server starts accepting, or a future systemd
// socket-activation setup. Start is what cmd/webhookserver uses; Serve
// exists so tests don't have to race NewServer's own internal bind.
func (s *Server) Serve(ln net.Listener) error {
	return s.httpServer.Serve(ln)
}

// Addr reports the address the server is configured to listen on
// (useful in tests that bind an ephemeral port via ":0").
func (s *Server) Addr() string {
	return s.httpServer.Addr
}

// Shutdown gracefully stops the server: no new connections are
// accepted, and in-flight requests get up to shutdownGracePeriod to
// finish before the underlying listener is forced closed. ctx's own
// deadline, if any, is still respected -- Shutdown returns as soon as
// either bound is hit.
func (s *Server) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, shutdownGracePeriod)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}
