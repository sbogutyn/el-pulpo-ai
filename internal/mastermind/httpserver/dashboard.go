package httpserver

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
)

// dashboardData is what the dashboard.html / dashboard_fragment.html templates
// render. The state counts are precomputed in the handler so the template
// doesn't have to walk Workers twice.
type dashboardData struct {
	Title        string
	Queue        []store.Task
	Workers      []store.DashboardWorker
	BusyCount    int
	IdleCount    int
	OfflineCount int
}

const (
	dashboardLogsPerWorker = 4
	idleWindow             = 60 * time.Second
	// dashboardWorkerStaleAfter hides workers that haven't checked in for this
	// long. Mastermind has no worker registry — every claimed_by ever seen
	// shows up via ListWorkers, so without a window the agent grid grows a
	// long tail of ghosts from old runs.
	dashboardWorkerStaleAfter = 1 * time.Hour

	agentRecentTasksLimit = 10
	agentLogTailLimit     = 500
)

func (s *Server) registerDashboardRoutes() {
	protected := auth.BasicAuth(s.cfg.AdminUser, s.cfg.AdminPassword)
	s.mux.Handle("/dashboard", protected(http.HandlerFunc(s.dashboardPage)))
	s.mux.Handle("/dashboard/fragment", protected(http.HandlerFunc(s.dashboardFragment)))
	// /dashboard/agents/{id} and /dashboard/agents/{id}/fragment.
	s.mux.Handle("/dashboard/agents/", protected(http.HandlerFunc(s.agentDetailRouter)))
}

func (s *Server) dashboardPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data := s.buildDashboardData(r)
	if err := s.pages["dashboard"].ExecuteTemplate(w, "dashboard", data); err != nil {
		s.log.Error("render dashboard", "error", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func (s *Server) dashboardFragment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	data := s.buildDashboardData(r)
	if err := s.pages["dashboard_fragment"].ExecuteTemplate(w, "dashboard_fragment", data); err != nil {
		s.log.Error("render dashboard_fragment", "error", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func (s *Server) buildDashboardData(r *http.Request) dashboardData {
	snap, err := s.store.GetDashboard(r.Context(), dashboardLogsPerWorker, dashboardWorkerStaleAfter)
	if err != nil {
		s.log.Error("dashboard snapshot", "error", err)
	}
	d := dashboardData{
		Title:   "Dashboard",
		Queue:   snap.Queue,
		Workers: snap.Workers,
	}
	now := time.Now().UTC()
	for _, w := range snap.Workers {
		switch agentStateName(now, w.Info.LastSeenAt, w.CurrentTask) {
		case "busy":
			d.BusyCount++
		case "idle":
			d.IdleCount++
		default:
			d.OfflineCount++
		}
	}
	return d
}

// ── agent detail ───────────────────────────────────────────────────

type agentDetailData struct {
	Title  string
	Detail store.AgentDetail
}

// agentDetailRouter splits /dashboard/agents/{id} from /dashboard/agents/{id}/fragment.
// A bare /dashboard/agents/ (no id) 404s.
func (s *Server) agentDetailRouter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Use RawPath when present so a worker id containing %2F (an encoded
	// slash) isn't split between id and verb after URL decoding. Falls back
	// to Path for the common case where there's nothing escaped.
	raw := r.URL.RawPath
	if raw == "" {
		raw = r.URL.Path
	}
	rest := strings.TrimPrefix(raw, "/dashboard/agents/")
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		http.NotFound(w, r)
		return
	}
	idEsc, verb, _ := strings.Cut(rest, "/")
	id, err := url.PathUnescape(idEsc)
	if err != nil || id == "" {
		http.NotFound(w, r)
		return
	}
	switch verb {
	case "":
		s.agentDetailPage(w, r, id)
	case "fragment":
		s.agentDetailFragment(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) agentDetailPage(w http.ResponseWriter, r *http.Request, id string) {
	data, ok := s.buildAgentDetailData(w, r, id)
	if !ok {
		return
	}
	if err := s.pages["agent_detail"].ExecuteTemplate(w, "agent_detail", data); err != nil {
		s.log.Error("render agent_detail", "error", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func (s *Server) agentDetailFragment(w http.ResponseWriter, r *http.Request, id string) {
	data, ok := s.buildAgentDetailData(w, r, id)
	if !ok {
		return
	}
	if err := s.pages["agent_detail_fragment"].ExecuteTemplate(w, "agent_detail_fragment", data); err != nil {
		s.log.Error("render agent_detail_fragment", "error", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func (s *Server) buildAgentDetailData(w http.ResponseWriter, r *http.Request, id string) (agentDetailData, bool) {
	d, err := s.store.GetAgentDetail(r.Context(), id, agentRecentTasksLimit, agentLogTailLimit)
	if errors.Is(err, store.ErrAgentNotFound) {
		http.NotFound(w, r)
		return agentDetailData{}, false
	}
	if err != nil {
		s.log.Error("agent detail", "error", err, "worker_id", id)
		http.Error(w, "render error", http.StatusInternalServerError)
		return agentDetailData{}, false
	}
	return agentDetailData{
		Title:  id,
		Detail: d,
	}, true
}

// ── template funcs ─────────────────────────────────────────────────

func dashboardFuncs() map[string]any {
	return map[string]any{
		"priorityClass": priorityClass,
		"shortID":       shortID,
		"relTime":       relTime,
		"agentState":    agentStateForTemplate,
		"initials":      initials,
		"deref_time":    derefTime,
		"attemptPct":    attemptPct,
		"logTime":       logTime,
		"urlEscape":     url.PathEscape,
	}
}

// priorityClass buckets the task's numeric priority into the wireframe's
// low/med/high classes for the priority-bar glyph.
func priorityClass(p int) string {
	switch {
	case p >= 7:
		return "high"
	case p >= 3:
		return "med"
	default:
		return "low"
	}
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// relTime renders a compact "Ns ago" / "Nm ago" / "Nh ago" / "Nd ago" form
// suitable for dense card layouts. Future timestamps render as "soon".
func relTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	if d < 0 {
		return "soon"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func logTime(t time.Time) string { return t.Local().Format("15:04:05") }

// agentStateForTemplate is the funcmap shim — the template can't call a
// function that takes a time.Time pointer plus a *store.Task directly without
// a wrapper, so route through this.
func agentStateForTemplate(lastSeen *time.Time, current *store.Task) string {
	return agentStateName(time.Now().UTC(), lastSeen, current)
}

func agentStateName(now time.Time, lastSeen *time.Time, current *store.Task) string {
	if current != nil {
		return "busy"
	}
	if lastSeen != nil && now.Sub(lastSeen.UTC()) <= idleWindow {
		return "idle"
	}
	return "offline"
}

// initials picks up to two characters from the worker ID for the avatar
// circle. Hyphens and underscores act as word separators (worker-A-1 → WA),
// otherwise the first two letters/digits are used.
func initials(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "??"
	}
	parts := strings.FieldsFunc(id, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})
	pick := func(s string) rune {
		for _, r := range s {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToUpper(r)
			}
		}
		return 0
	}
	out := make([]rune, 0, 2)
	for _, p := range parts {
		if r := pick(p); r != 0 {
			out = append(out, r)
			if len(out) == 2 {
				break
			}
		}
	}
	if len(out) == 0 {
		return "??"
	}
	if len(out) == 1 {
		// Fall back to the first two characters of the original string.
		runes := []rune(id)
		if len(runes) >= 2 {
			return strings.ToUpper(string(runes[:2]))
		}
		return strings.ToUpper(string(runes))
	}
	return string(out)
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// attemptPct maps attempt_count / max_attempts to a 0..100 progress fill.
// Mostly visual — there's no real "% complete" signal in the queue model, so
// this is a stand-in that reflects retry progress.
func attemptPct(attempt, max int) int {
	if max <= 0 {
		return 0
	}
	pct := float64(attempt) / float64(max) * 100
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return int(math.Round(pct))
}
