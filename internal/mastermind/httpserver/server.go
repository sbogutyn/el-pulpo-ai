// Package httpserver implements the admin UI, health probes, and metrics endpoint for the mastermind.
package httpserver

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
)

//go:embed all:templates
var templatesFS embed.FS

//go:embed all:static
var staticFS embed.FS

type Config struct {
	AdminUser     string
	AdminPassword string
}

type Server struct {
	store *store.Store
	cfg   Config
	log   *slog.Logger
	tpl   *template.Template
	mux   *http.ServeMux
}

func New(s *store.Store, cfg Config, log *slog.Logger) (*Server, error) {
	tpl, err := template.ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	srv := &Server{store: s, cfg: cfg, log: log, tpl: tpl, mux: http.NewServeMux()}
	srv.routes()
	return srv, nil
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	s.mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.Ping(r.Context()); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ready"))
	})
	s.mux.Handle("/metrics", promhttp.Handler())

	sub, err := fsSub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	static := http.FileServer(sub)
	s.mux.Handle("/static/", auth.BasicAuth(s.cfg.AdminUser, s.cfg.AdminPassword)(http.StripPrefix("/static/", static)))

	s.registerTasksRoutes()

	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/tasks", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})
}

// ListenAndServe runs the HTTP server until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	hs := &http.Server{Addr: addr, Handler: s.mux}
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http: listening", "addr", addr)
		err := hs.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdownCtx)
		return <-errCh
	case err := <-errCh:
		return err
	}
}
