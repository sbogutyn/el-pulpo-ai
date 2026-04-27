package grpcserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// AdminServer implements the AdminService RPCs by delegating to the store.
// No new SQL is introduced — every call is one existing store method.
type AdminServer struct {
	pb.UnimplementedAdminServiceServer
	store *store.Store
}

func NewAdmin(s *store.Store) *AdminServer { return &AdminServer{store: s} }

const maxNameLen = 200

// validateInstructions ensures the create-time payload carries a non-empty
// `instructions` text. The text is the canonical "what should the agent do"
// surface; the rest of the payload remains opaque.
func validateInstructions(payload []byte) error {
	if len(payload) == 0 {
		return errors.New(`payload.instructions must be a non-empty string`)
	}
	var v struct {
		Instructions *string `json:"instructions"`
	}
	if err := json.Unmarshal(payload, &v); err != nil {
		return fmt.Errorf("payload is not valid JSON: %w", err)
	}
	if v.Instructions == nil {
		return errors.New(`payload.instructions must be a non-empty string`)
	}
	if strings.TrimSpace(*v.Instructions) == "" {
		return errors.New(`payload.instructions must be a non-empty string`)
	}
	return nil
}

func (a *AdminServer) CreateTask(ctx context.Context, req *pb.CreateTaskRequest) (*pb.CreateTaskResponse, error) {
	name := req.GetName()
	if name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if len(name) > maxNameLen {
		return nil, status.Errorf(codes.InvalidArgument, "name too long (max %d)", maxNameLen)
	}
	if req.GetMaxAttempts() < 0 || req.GetMaxAttempts() > 50 {
		return nil, status.Error(codes.InvalidArgument, "max_attempts must be 0 (default) or 1..50")
	}

	var payload json.RawMessage
	if len(req.GetPayload()) > 0 {
		payload = json.RawMessage(req.GetPayload())
		var tmp any
		if err := json.Unmarshal(payload, &tmp); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "payload is not valid JSON: %v", err)
		}
	}
	if err := validateInstructions(payload); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	in := store.NewTaskInput{
		Name:        name,
		Payload:     payload,
		Priority:    int(req.GetPriority()),
		MaxAttempts: int(req.GetMaxAttempts()),
	}
	if sf := req.GetScheduledFor(); sf != nil {
		t := sf.AsTime()
		in.ScheduledFor = &t
	}
	if v := req.GetJiraUrl(); v != "" {
		in.JiraURL = &v
	}
	if v := req.GetGithubPrUrl(); v != "" {
		in.GithubPRURL = &v
	}

	t, err := a.store.CreateTask(ctx, in)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create: %v", err)
	}
	return &pb.CreateTaskResponse{Task: toTaskDetail(t)}, nil
}

// toTaskDetail converts a store.Task into the proto TaskDetail. Nullable
// columns become empty strings or nil timestamps; MCP layer downstream omits
// them when serializing to JSON.
func toTaskDetail(t store.Task) *pb.TaskDetail {
	d := &pb.TaskDetail{
		Id:           t.ID.String(),
		Name:         t.Name,
		Payload:      []byte(t.Payload),
		Priority:     int32(t.Priority),
		Status:       string(t.Status),
		AttemptCount: int32(t.AttemptCount),
		MaxAttempts:  int32(t.MaxAttempts),
		CreatedAt:    timestamppb.New(t.CreatedAt),
		UpdatedAt:    timestamppb.New(t.UpdatedAt),
	}
	if t.ScheduledFor != nil {
		d.ScheduledFor = timestamppb.New(*t.ScheduledFor)
	}
	if t.ClaimedBy != nil {
		d.ClaimedBy = *t.ClaimedBy
	}
	if t.ClaimedAt != nil {
		d.ClaimedAt = timestamppb.New(*t.ClaimedAt)
	}
	if t.LastHeartbeatAt != nil {
		d.LastHeartbeatAt = timestamppb.New(*t.LastHeartbeatAt)
	}
	if t.CompletedAt != nil {
		d.CompletedAt = timestamppb.New(*t.CompletedAt)
	}
	if t.LastError != nil {
		d.LastError = *t.LastError
	}
	if t.JiraURL != nil {
		d.JiraUrl = *t.JiraURL
	}
	if t.GithubPRURL != nil {
		d.GithubPrUrl = *t.GithubPRURL
	}
	return d
}

func (a *AdminServer) GetTask(ctx context.Context, req *pb.GetTaskRequest) (*pb.GetTaskResponse, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "id must be a UUID")
	}
	t, err := a.store.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "task %s not found", id)
		}
		return nil, status.Errorf(codes.Internal, "get: %v", err)
	}
	return &pb.GetTaskResponse{Task: toTaskDetail(t)}, nil
}

var knownStatuses = map[string]store.TaskStatus{
	"pending":          store.StatusPending,
	"claimed":          store.StatusClaimed,
	"in_progress":      store.StatusInProgress,
	"pr_opened":        store.StatusPROpened,
	"review_requested": store.StatusReviewRequested,
	"completed":        store.StatusCompleted,
	"failed":           store.StatusFailed,
}

func (a *AdminServer) ListTaskLogs(ctx context.Context, req *pb.ListTaskLogsRequest) (*pb.ListTaskLogsResponse, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "id must be a UUID")
	}
	if req.GetLimit() < 0 || req.GetLimit() > 10000 {
		return nil, status.Error(codes.InvalidArgument, "limit must be 0..10000")
	}
	// Make sure the id refers to an actual task so an unknown id returns
	// NOT_FOUND rather than an empty list (which would be indistinguishable
	// from a task with no logs).
	if _, err := a.store.GetTask(ctx, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "task %s not found", id)
		}
		return nil, status.Errorf(codes.Internal, "get: %v", err)
	}
	entries, err := a.store.ListTaskLogs(ctx, id, int(req.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list_task_logs: %v", err)
	}
	out := &pb.ListTaskLogsResponse{}
	for _, e := range entries {
		out.Items = append(out.Items, &pb.TaskLogEntry{
			Id:        e.ID,
			TaskId:    e.TaskID.String(),
			Message:   e.Message,
			CreatedAt: timestamppb.New(e.CreatedAt),
		})
	}
	return out, nil
}

func (a *AdminServer) CancelTask(ctx context.Context, req *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "id must be a UUID")
	}
	switch err := a.store.DeleteTask(ctx, id); {
	case err == nil:
		return &pb.CancelTaskResponse{}, nil
	case errors.Is(err, store.ErrNotFound):
		return nil, status.Errorf(codes.NotFound, "task %s not found", id)
	case errors.Is(err, store.ErrNotDeletable):
		return nil, status.Error(codes.FailedPrecondition, "cannot cancel an active task (claimed or in_progress)")
	default:
		return nil, status.Errorf(codes.Internal, "cancel: %v", err)
	}
}

func (a *AdminServer) RetryTask(ctx context.Context, req *pb.RetryTaskRequest) (*pb.RetryTaskResponse, error) {
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "id must be a UUID")
	}
	t, err := a.store.RequeueTask(ctx, id)
	switch {
	case err == nil:
		return &pb.RetryTaskResponse{Task: toTaskDetail(t)}, nil
	case errors.Is(err, store.ErrNotFound):
		return nil, status.Errorf(codes.NotFound, "task %s not found", id)
	case errors.Is(err, store.ErrNotRequeueable):
		return nil, status.Error(codes.FailedPrecondition, "cannot retry an active task (claimed or in_progress)")
	default:
		return nil, status.Errorf(codes.Internal, "retry: %v", err)
	}
}

func (a *AdminServer) ListWorkers(ctx context.Context, _ *pb.ListWorkersRequest) (*pb.ListWorkersResponse, error) {
	workers, err := a.store.ListWorkers(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list_workers: %v", err)
	}
	out := &pb.ListWorkersResponse{}
	for _, w := range workers {
		info := &pb.WorkerInfo{
			Id:             w.ID,
			ActiveTasks:    int32(w.ActiveTasks),
			CompletedTasks: int32(w.CompletedTasks),
			FailedTasks:    int32(w.FailedTasks),
		}
		if w.LastSeenAt != nil {
			info.LastSeenAt = timestamppb.New(*w.LastSeenAt)
		}
		out.Items = append(out.Items, info)
	}
	return out, nil
}

func (a *AdminServer) ListTasks(ctx context.Context, req *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	f := store.ListTasksFilter{
		Limit:  int(req.GetLimit()),
		Offset: int(req.GetOffset()),
	}
	if req.GetLimit() < 0 || req.GetLimit() > 500 {
		return nil, status.Error(codes.InvalidArgument, "limit must be 0..500")
	}
	if req.GetOffset() < 0 {
		return nil, status.Error(codes.InvalidArgument, "offset must be non-negative")
	}
	if s := req.GetStatus(); s != "" {
		ks, ok := knownStatuses[s]
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "unknown status %q", s)
		}
		f.Status = &ks
	}
	page, err := a.store.ListTasks(ctx, f)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list: %v", err)
	}
	out := &pb.ListTasksResponse{Total: int32(page.Total)}
	for _, t := range page.Items {
		out.Items = append(out.Items, toTaskDetail(t))
	}
	return out, nil
}
