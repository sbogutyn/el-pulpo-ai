package httpserver

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

func TestWireframes_RedirectsToBundle(t *testing.T) {
	srv := newServer(t)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/wireframes", ""))
	if rr.Code != http.StatusFound {
		t.Fatalf("code=%d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != "/static/wireframes/" {
		t.Errorf("Location=%q, want /static/wireframes/", loc)
	}
}

func TestWireframes_BundleIsServed(t *testing.T) {
	srv := newServer(t)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/static/wireframes/", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, marker := range []string{
		"Task Queue · Wireframes",
		"design-canvas.jsx",
		"wireframes.css",
		"wf1.jsx",
		"wf5.jsx",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("bundle missing marker %q", marker)
		}
	}
}

func TestWireframes_AssetsAreServed(t *testing.T) {
	srv := newServer(t)
	for path, marker := range map[string]string{
		"/static/wireframes/wireframes.css":   "--accent",
		"/static/wireframes/wireframes.jsx":   "TASKS",
		"/static/wireframes/wf1.jsx":          "Wireframe1",
		"/static/wireframes/design-canvas.jsx": "DesignCanvas",
	} {
		rr := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, path, ""))
		if rr.Code != http.StatusOK {
			t.Errorf("%s code=%d", path, rr.Code)
			continue
		}
		if !strings.Contains(rr.Body.String(), marker) {
			t.Errorf("%s missing marker %q", path, marker)
		}
	}
}

func TestWireframes_RequiresAuth(t *testing.T) {
	srv := newServer(t)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/wireframes", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("code=%d, want 401", rr.Code)
	}
}
