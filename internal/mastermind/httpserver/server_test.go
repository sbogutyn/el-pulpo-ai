package httpserver

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
)

func newServer(t *testing.T) *Server {
	t.Helper()
	s, err := store.Open(t.Context(), testDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	srv, err := New(s, Config{AdminUser: "u", AdminPassword: "p"}, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestHealthz(t *testing.T) {
	srv := newServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("code=%d", rr.Code)
	}
}

func TestReadyz(t *testing.T) {
	srv := newServer(t)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("code=%d", rr.Code)
	}
}
