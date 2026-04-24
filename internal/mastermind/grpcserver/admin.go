package grpcserver

import (
	"context"
	"encoding/json"

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
	return d
}
