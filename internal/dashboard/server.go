package dashboard

import (
	"context"
	"net"
	"net/http"
	"time"
)

// shutdownGracePeriod mirrors internal/webhook.Server's constant of the
// same name and purpose.
const shutdownGracePeriod = 10 * time.Second

// Server wraps an http.Server bound to a Handler's routes. Mirrors
// internal/webhook.Server's exact shape so cmd/dashboard/main.go reads
// the same way cmd/webhookserver/main.go does.
type Server struct {
	httpServer *http.Server
}

// NewServer wires h's routes into a fresh http.ServeMux and returns a
// Server ready to Start/Serve.
func NewServer(addr string, h *Handler) *Server {
	mux := http.NewServeMux()
	h.Register(mux)

	return &Server{
		httpServer: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
	}
}

func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

func (s *Server) Serve(ln net.Listener) error {
	return s.httpServer.Serve(ln)
}

func (s *Server) Addr() string {
	return s.httpServer.Addr
}

func (s *Server) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, shutdownGracePeriod)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}
