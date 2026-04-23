package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestBasicAuth_Challenges(t *testing.T) {
	h := BasicAuth("u", "p")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("code=%d, want 401", rr.Code)
	}
	if got := rr.Header().Get("WWW-Authenticate"); got == "" {
		t.Errorf("missing WWW-Authenticate header")
	}
}

func TestBasicAuth_RejectsWrongCreds(t *testing.T) {
	h := BasicAuth("u", "p")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("u", "wrong")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("code=%d, want 401", rr.Code)
	}
}

func TestBasicAuth_Allows(t *testing.T) {
	h := BasicAuth("u", "p")(okHandler())
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("u", "p")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("code=%d, want 200", rr.Code)
	}
}
