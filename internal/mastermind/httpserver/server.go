// Package httpserver implements the admin UI, health probes, and metrics endpoint for the mastermind.
package httpserver

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/issuerefs"
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
	pages map[string]*template.Template
	mux   *http.ServeMux
}

func New(s *store.Store, cfg Config, log *slog.Logger) (*Server, error) {
	funcs := template.FuncMap{
		"jiraShort": issuerefs.JiraShort,
		"prShort":   issuerefs.PRShort,
	}

	pages := map[string]*template.Template{}
	pageFiles := map[string][]string{
		"tasks_list":   {"templates/base.html", "templates/tasks_list.html", "templates/tasks_fragment.html"},
		"tasks_new":    {"templates/base.html", "templates/tasks_new.html"},
		"tasks_edit":   {"templates/base.html", "templates/tasks_edit.html"},
		"tasks_detail": {"templates/base.html", "templates/tasks_detail.html"},
	}
	for name, files := range pageFiles {
		t, err := template.New("").Funcs(funcs).ParseFS(templatesFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		pages[name] = t
	}
	fragTree, err := template.New("").Funcs(funcs).ParseFS(templatesFS, "templates/tasks_fragment.html")
	if err != nil {
		return nil, fmt.Errorf("parse tasks_fragment: %w", err)
	}
	pages["tasks_fragment"] = fragTree

	srv := &Server{store: s, cfg: cfg, log: log, pages: pages, mux: http.NewServeMux()}
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
