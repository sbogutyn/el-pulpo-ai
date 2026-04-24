package mcpserver

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// HTTPConfig configures [Serve].
type HTTPConfig struct {
	// Addr is the listen address for the MCP HTTP endpoint. Default
	// "127.0.0.1:7777"; callers are encouraged to keep it on loopback to
	// benefit from the SDK's DNS-rebinding protection.
	Addr string

	// ShutdownTimeout caps how long Serve waits for in-flight requests to
	// drain after the ctx is cancelled. Default 5s.
	ShutdownTimeout time.Duration
}

// Serve runs an HTTP MCP endpoint backed by the given [State] until ctx is
// cancelled. The returned error is whatever http.Server.Serve returned, except
// that http.ErrServerClosed after a graceful shutdown is converted to nil.
func Serve(ctx context.Context, st *State, cfg HTTPConfig, log *slog.Logger) error {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:7777"
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 5 * time.Second
	}

	server := NewServer(st)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	lis, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	hs := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("worker-mcp: listening", "addr", lis.Addr().String())
		errCh <- hs.Serve(lis)
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		_ = hs.Shutdown(shutdownCtx)
		<-errCh
		return nil
	}
}
