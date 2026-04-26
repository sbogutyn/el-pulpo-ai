package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
)

func TestDashboard_Page_RequiresAuth(t *testing.T) {
	srv := newServer(t)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/dashboard", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("code=%d, want 401", rr.Code)
	}
}

func TestDashboard_Fragment_RequiresAuth(t *testing.T) {
	srv := newServer(t)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/dashboard/fragment", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("code=%d, want 401", rr.Code)
	}
}

func TestDashboard_Page_RendersChrome(t *testing.T) {
	srv := newServer(t)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/dashboard", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, marker := range []string{
		`<title>Dashboard — mastermind</title>`,
		`href="/static/dashboard.css"`,
		`hx-get="/dashboard/fragment"`,
		`class="agents-grid"`,
		`class="queue-list"`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("dashboard page missing marker %q", marker)
		}
	}
}

func TestDashboard_Fragment_ShowsQueueAndWorkers(t *testing.T) {
	srv := newServer(t)
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()
	if _, err := s.CreateTask(ctx, store.NewTaskInput{Name: "high-prio-task", Priority: 9, MaxAttempts: 3}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	created, err := s.CreateTask(ctx, store.NewTaskInput{Name: "to-be-claimed", Priority: 5, MaxAttempts: 3})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	claimed, err := s.ClaimTask(ctx, "worker-alpha")
	if err != nil || claimed == nil {
		t.Fatalf("ClaimTask: %v %v", err, claimed)
	}
	if _, err := s.AppendTaskLog(ctx, "worker-alpha", claimed.ID, "doing the thing"); err != nil {
		t.Fatalf("AppendTaskLog: %v", err)
	}
	_ = created

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/dashboard/fragment", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()

	for _, marker := range []string{
		"high-prio-task", // pending task in the queue
		"worker-alpha",   // worker name
		"doing the thing", // log line
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("dashboard fragment missing marker %q\nbody:\n%s", marker, body)
		}
	}
	// Workers with claimed tasks should render as busy.
	if !strings.Contains(body, "dot busy") {
		t.Errorf("expected 'dot busy' state for the working agent: %s", body)
	}
}

func TestDashboard_Fragment_EmptyState(t *testing.T) {
	srv := newServer(t)
	s, _ := store.Open(context.Background(), testDSN)
	defer s.Close()
	if _, err := s.Pool().Exec(context.Background(), "TRUNCATE TABLE tasks CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/dashboard/fragment", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "— nothing queued —") {
		t.Errorf("missing empty-queue copy: %s", body)
	}
	if !strings.Contains(body, "no workers have ever checked in") {
		t.Errorf("missing empty-workers copy: %s", body)
	}
}

func TestDashboard_AgentState_TransitionsByLastSeen(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		current  *store.Task
		lastSeen *time.Time
		want     string
	}{
		{"busy when holding a task", &store.Task{}, ptrTime(now.Add(-5 * time.Second)), "busy"},
		{"idle within window", nil, ptrTime(now.Add(-30 * time.Second)), "idle"},
		{"offline past window", nil, ptrTime(now.Add(-5 * time.Minute)), "offline"},
		{"offline when never seen", nil, nil, "offline"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := agentStateName(now, c.lastSeen, c.current); got != c.want {
				t.Errorf("got=%q, want=%q", got, c.want)
			}
		})
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestDashboard_Initials(t *testing.T) {
	cases := map[string]string{
		"worker-alpha": "WA",
		"orca-01":      "O0",
		"FN":           "FN",
		"":             "??",
		"single":       "SI",
	}
	for in, want := range cases {
		if got := initials(in); got != want {
			t.Errorf("initials(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestDashboard_AttemptPct(t *testing.T) {
	if got := attemptPct(0, 0); got != 0 {
		t.Errorf("zero/zero=%d, want 0", got)
	}
	if got := attemptPct(2, 4); got != 50 {
		t.Errorf("2/4=%d, want 50", got)
	}
	if got := attemptPct(99, 4); got != 100 {
		t.Errorf("clamped above 100=%d, want 100", got)
	}
}

func TestDashboard_PriorityClass(t *testing.T) {
	if priorityClass(8) != "high" {
		t.Errorf("p=8 should be high")
	}
	if priorityClass(5) != "med" {
		t.Errorf("p=5 should be med")
	}
	if priorityClass(0) != "low" {
		t.Errorf("p=0 should be low")
	}
}

// ── /dashboard/agents/{id} ────────────────────────────────────────

func TestAgentDetail_RequiresAuth(t *testing.T) {
	srv := newServer(t)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/dashboard/agents/anything", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("code=%d, want 401", rr.Code)
	}
}

func TestAgentDetail_NotFoundForUnknownID(t *testing.T) {
	srv := newServer(t)
	s, _ := store.Open(context.Background(), testDSN)
	defer s.Close()
	if _, err := s.Pool().Exec(context.Background(), "TRUNCATE TABLE tasks CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/dashboard/agents/ghost", ""))
	if rr.Code != http.StatusNotFound {
		t.Errorf("code=%d, want 404", rr.Code)
	}
}

func TestAgentDetail_NotFoundForBareRoute(t *testing.T) {
	srv := newServer(t)
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/dashboard/agents/", ""))
	if rr.Code != http.StatusNotFound {
		t.Errorf("code=%d, want 404", rr.Code)
	}
}

func TestAgentDetail_RendersPageAndFragment(t *testing.T) {
	srv := newServer(t)
	s, _ := store.Open(context.Background(), testDSN)
	defer s.Close()
	if _, err := s.Pool().Exec(context.Background(), "TRUNCATE TABLE tasks CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ctx := context.Background()

	if _, err := s.CreateTask(ctx, store.NewTaskInput{Name: "summarize-call-transcripts", Priority: 5, MaxAttempts: 3}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	claimed, err := s.ClaimTask(ctx, "orca-01")
	if err != nil || claimed == nil {
		t.Fatalf("ClaimTask: %v %v", err, claimed)
	}
	for _, msg := range []string{"step 1", "step 2", "wrap up"} {
		if _, err := s.AppendTaskLog(ctx, "orca-01", claimed.ID, msg); err != nil {
			t.Fatalf("AppendTaskLog: %v", err)
		}
	}

	// Full page.
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/dashboard/agents/orca-01", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("page code=%d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, marker := range []string{
		"<title>orca-01 — agent — mastermind</title>",
		`hx-get="/dashboard/agents/orca-01/fragment"`,
		`id="logs-scroll"`,
		"summarize-call-transcripts",
		"step 1",
		"wrap up",
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("page missing marker %q", marker)
		}
	}

	// Fragment alone (the HTMX swap target).
	rr = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/dashboard/agents/orca-01/fragment", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("fragment code=%d body=%s", rr.Code, rr.Body.String())
	}
	frag := rr.Body.String()
	if strings.Contains(frag, "<title>") {
		t.Error("fragment should not contain <title>; it's an inner partial")
	}
	if !strings.Contains(frag, "step 2") {
		t.Errorf("fragment missing log marker; body=%s", frag)
	}
}

func TestAgentDetail_HandlesURLEncodedID(t *testing.T) {
	srv := newServer(t)
	s, _ := store.Open(context.Background(), testDSN)
	defer s.Close()
	if _, err := s.Pool().Exec(context.Background(), "TRUNCATE TABLE tasks CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	ctx := context.Background()
	if _, err := s.CreateTask(ctx, store.NewTaskInput{Name: "x"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := s.ClaimTask(ctx, "weird id/with slash"); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, authedReq(http.MethodGet, "/dashboard/agents/weird%20id%2Fwith%20slash", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "weird id/with slash") {
		t.Errorf("expected decoded id in body")
	}
}
