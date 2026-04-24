package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/issuerefs"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
)

type taskForm struct {
	Name         string
	Priority     int
	MaxAttempts  int
	ScheduledFor string
	Payload      string
	JiraURL      string
	GithubPRURL  string
}

type listPageData struct {
	Title         string
	Items         []store.Task
	Total         int
	Statuses      []store.TaskStatus
	CurrentStatus store.TaskStatus
}

type formPageData struct {
	Title string
	Form  taskForm
	Task  *store.Task
	Error string
}

type detailPageData struct {
	Title string
	Task  store.Task
	Logs  []store.TaskLogEntry
	Error string
}

func (s *Server) registerTasksRoutes() {
	protected := auth.BasicAuth(s.cfg.AdminUser, s.cfg.AdminPassword)

	s.mux.Handle("/tasks", protected(http.HandlerFunc(s.tasksCollection)))
	s.mux.Handle("/tasks/", protected(http.HandlerFunc(s.tasksMember)))
	s.mux.Handle("/tasks/fragment", protected(http.HandlerFunc(s.tasksFragment)))
	s.mux.Handle("/tasks/new", protected(http.HandlerFunc(s.tasksNewForm)))
}

func (s *Server) tasksCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.tasksList(w, r)
	case http.MethodPost:
		s.tasksCreate(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) tasksList(w http.ResponseWriter, r *http.Request) {
	data := s.buildListData(r)
	if err := s.pages["tasks_list"].ExecuteTemplate(w, "base", data); err != nil {
		s.log.Error("render list", "error", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func (s *Server) tasksFragment(w http.ResponseWriter, r *http.Request) {
	data := s.buildListData(r)
	if err := s.pages["tasks_fragment"].ExecuteTemplate(w, "tasks_fragment", data); err != nil {
		s.log.Error("render tasks_fragment", "error", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func (s *Server) buildListData(r *http.Request) listPageData {
	status := store.TaskStatus(r.URL.Query().Get("status"))
	filter := store.ListTasksFilter{Limit: 200}
	if status != "" {
		filter.Status = &status
	}
	page, err := s.store.ListTasks(r.Context(), filter)
	if err != nil {
		s.log.Error("list tasks", "error", err)
	}
	return listPageData{
		Title:         "Tasks",
		Items:         page.Items,
		Total:         page.Total,
		Statuses:      []store.TaskStatus{store.StatusPending, store.StatusClaimed, store.StatusRunning, store.StatusCompleted, store.StatusFailed},
		CurrentStatus: status,
	}
}

func (s *Server) tasksNewForm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.pages["tasks_new"].ExecuteTemplate(w, "base", formPageData{
		Title: "New task",
		Form:  taskForm{MaxAttempts: 3, Payload: "{}"},
	}); err != nil {
		s.log.Error("render tasks_new", "error", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func (s *Server) tasksCreate(w http.ResponseWriter, r *http.Request) {
	form, input, err := parseTaskForm(r)
	if err != nil {
		renderFormError(w, s, "tasks_new", formPageData{Title: "New task", Form: form, Error: err.Error()}, http.StatusBadRequest)
		return
	}
	if _, err := s.store.CreateTask(r.Context(), input); err != nil {
		renderFormError(w, s, "tasks_new", formPageData{Title: "New task", Form: form, Error: err.Error()}, http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/tasks", http.StatusSeeOther)
}

func (s *Server) tasksMember(w http.ResponseWriter, r *http.Request) {
	// /tasks/{id} | /tasks/{id}/edit | /tasks/{id}/delete | /tasks/{id}/requeue
	rest := r.URL.Path[len("/tasks/"):]

	// Strip trailing verbs.
	var verb, idStr string
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		idStr = rest[:slash]
		verb = rest[slash+1:]
	} else {
		idStr = rest
	}

	id, err := uuid.Parse(idStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	switch {
	case verb == "" && r.Method == http.MethodGet:
		s.tasksDetail(w, r, id)
	case verb == "" && r.Method == http.MethodPost:
		s.tasksUpdate(w, r, id)
	case verb == "edit" && r.Method == http.MethodGet:
		s.tasksEditForm(w, r, id)
	case verb == "delete" && r.Method == http.MethodPost:
		s.tasksDelete(w, r, id)
	case verb == "requeue" && r.Method == http.MethodPost:
		s.tasksRequeue(w, r, id)
	case verb == "links" && r.Method == http.MethodPost:
		s.tasksUpdateLinks(w, r, id)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) tasksDetail(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	task, err := s.store.GetTask(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logs, err := s.store.ListTaskLogs(r.Context(), id, 500)
	if err != nil {
		s.log.Warn("list task logs", "error", err, "task_id", id)
	}
	if err := s.pages["tasks_detail"].ExecuteTemplate(w, "base", detailPageData{Title: task.Name, Task: task, Logs: logs}); err != nil {
		s.log.Error("render tasks_detail", "error", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func (s *Server) tasksEditForm(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	task, err := s.store.GetTask(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.pages["tasks_edit"].ExecuteTemplate(w, "base", formPageData{
		Title: "Edit " + task.Name,
		Task:  &task,
		Form:  formFromTask(task),
	}); err != nil {
		s.log.Error("render tasks_edit", "error", err)
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func (s *Server) tasksUpdate(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	form, input, err := parseTaskForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_, err = s.store.UpdateTask(r.Context(), id, store.UpdateTaskInput{
		Name: input.Name, Priority: input.Priority,
		MaxAttempts: input.MaxAttempts, ScheduledFor: input.ScheduledFor,
		Payload:     input.Payload,
		JiraURL:     input.JiraURL,
		GithubPRURL: input.GithubPRURL,
	})
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if errors.Is(err, store.ErrNotEditable) {
		renderFormError(w, s, "tasks_edit", formPageData{Title: "Edit", Form: form, Error: "task is not pending — cannot edit"}, http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/tasks/"+id.String(), http.StatusSeeOther)
}

func (s *Server) tasksUpdateLinks(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jira := strings.TrimSpace(r.FormValue("jira_url"))
	pr := strings.TrimSpace(r.FormValue("github_pr_url"))

	var jiraPtr, prPtr *string
	if jira != "" {
		if err := issuerefs.ValidateJira(jira); err != nil {
			s.renderDetailError(w, r, id, "JIRA URL must look like https://<host>/browse/PROJ-123", http.StatusBadRequest)
			return
		}
		jiraPtr = &jira
	}
	if pr != "" {
		if err := issuerefs.ValidatePR(pr); err != nil {
			s.renderDetailError(w, r, id, "GitHub PR URL must look like https://<host>/<org>/<repo>/pull/123", http.StatusBadRequest)
			return
		}
		prPtr = &pr
	}

	if _, err := s.store.UpdateTaskLinks(r.Context(), id, jiraPtr, prPtr); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/tasks/"+id.String(), http.StatusSeeOther)
}

func (s *Server) renderDetailError(w http.ResponseWriter, r *http.Request, id uuid.UUID, msg string, code int) {
	task, err := s.store.GetTask(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	logs, listErr := s.store.ListTaskLogs(r.Context(), id, 500)
	if listErr != nil {
		s.log.Warn("list task logs", "error", listErr, "task_id", id)
	}
	w.WriteHeader(code)
	if err := s.pages["tasks_detail"].ExecuteTemplate(w, "base", detailPageData{Title: task.Name, Task: task, Logs: logs, Error: msg}); err != nil {
		s.log.Error("render tasks_detail", "error", err)
	}
}

func (s *Server) tasksDelete(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	switch err := s.store.DeleteTask(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, store.ErrNotDeletable):
		http.Error(w, "cannot delete active task", http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		http.Redirect(w, r, "/tasks", http.StatusSeeOther)
	}
}

func (s *Server) tasksRequeue(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	switch _, err := s.store.RequeueTask(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, store.ErrNotRequeueable):
		http.Error(w, "cannot requeue active task", http.StatusConflict)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		if r.Header.Get("HX-Request") == "true" {
			s.tasksFragment(w, r)
			return
		}
		http.Redirect(w, r, "/tasks/"+id.String(), http.StatusSeeOther)
	}
}

// ---- helpers ----

func parseTaskForm(r *http.Request) (taskForm, store.NewTaskInput, error) {
	if err := r.ParseForm(); err != nil {
		return taskForm{}, store.NewTaskInput{}, err
	}
	f := taskForm{
		Name:         r.FormValue("name"),
		ScheduledFor: r.FormValue("scheduled_for"),
		Payload:      r.FormValue("payload"),
		JiraURL:      strings.TrimSpace(r.FormValue("jira_url")),
		GithubPRURL:  strings.TrimSpace(r.FormValue("github_pr_url")),
	}
	if s := r.FormValue("priority"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return f, store.NewTaskInput{}, errors.New("priority must be an integer")
		}
		f.Priority = n
	}
	if s := r.FormValue("max_attempts"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			return f, store.NewTaskInput{}, errors.New("max_attempts must be an integer")
		}
		f.MaxAttempts = n
	}
	if f.MaxAttempts <= 0 {
		f.MaxAttempts = 3
	}
	if f.Payload == "" {
		f.Payload = "{}"
	}
	if !json.Valid([]byte(f.Payload)) {
		return f, store.NewTaskInput{}, errors.New("payload must be valid JSON")
	}
	payloadJSON := json.RawMessage(f.Payload)

	var jiraPtr, prPtr *string
	if f.JiraURL != "" {
		if err := issuerefs.ValidateJira(f.JiraURL); err != nil {
			return f, store.NewTaskInput{}, errors.New("JIRA URL must look like https://<host>/browse/PROJ-123")
		}
		v := f.JiraURL
		jiraPtr = &v
	}
	if f.GithubPRURL != "" {
		if err := issuerefs.ValidatePR(f.GithubPRURL); err != nil {
			return f, store.NewTaskInput{}, errors.New("GitHub PR URL must look like https://<host>/<org>/<repo>/pull/123")
		}
		v := f.GithubPRURL
		prPtr = &v
	}

	input := store.NewTaskInput{
		Name:        f.Name,
		Priority:    f.Priority,
		MaxAttempts: f.MaxAttempts,
		Payload:     payloadJSON,
		JiraURL:     jiraPtr,
		GithubPRURL: prPtr,
	}
	if f.ScheduledFor != "" {
		t, err := time.ParseInLocation("2006-01-02T15:04", f.ScheduledFor, time.Local)
		if err != nil {
			return f, store.NewTaskInput{}, errors.New("scheduled_for must be YYYY-MM-DDTHH:MM")
		}
		input.ScheduledFor = &t
	}
	return f, input, nil
}

func formFromTask(t store.Task) taskForm {
	tf := taskForm{
		Name:        t.Name,
		Priority:    t.Priority,
		MaxAttempts: t.MaxAttempts,
		Payload:     string(t.Payload),
	}
	if t.ScheduledFor != nil {
		tf.ScheduledFor = t.ScheduledFor.In(time.Local).Format("2006-01-02T15:04")
	}
	if t.JiraURL != nil {
		tf.JiraURL = *t.JiraURL
	}
	if t.GithubPRURL != nil {
		tf.GithubPRURL = *t.GithubPRURL
	}
	return tf
}

func renderFormError(w http.ResponseWriter, s *Server, pageKey string, data formPageData, code int) {
	w.WriteHeader(code)
	if err := s.pages[pageKey].ExecuteTemplate(w, "base", data); err != nil {
		// headers already sent; can only log.
		s.log.Error("render form", "page", pageKey, "error", err)
	}
}
