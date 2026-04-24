// Package grpcserver implements the gRPC TaskService handlers.
package grpcserver

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/metrics"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

type Server struct {
	pb.UnimplementedTaskServiceServer
	store *store.Store
}

func New(s *store.Store) *Server { return &Server{store: s} }

func (s *Server) ClaimTask(ctx context.Context, req *pb.ClaimTaskRequest) (*pb.ClaimTaskResponse, error) {
	if req.GetWorkerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "worker_id is required")
	}
	start := time.Now()
	t, err := s.store.ClaimTask(ctx, req.GetWorkerId())
	metrics.ClaimDurationSeconds.Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.TasksClaimedTotal.WithLabelValues("error").Inc()
		return nil, status.Errorf(codes.Internal, "claim: %v", err)
	}
	if t == nil {
		metrics.TasksClaimedTotal.WithLabelValues("empty").Inc()
		return nil, status.Error(codes.NotFound, "no tasks available")
	}
	metrics.TasksClaimedTotal.WithLabelValues("success").Inc()
	return &pb.ClaimTaskResponse{Task: &pb.Task{
		Id:      t.ID.String(),
		Name:    t.Name,
		Payload: t.Payload,
	}}, nil
}

func (s *Server) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	id, err := uuid.Parse(req.GetTaskId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "task_id must be a UUID")
	}
	if err := s.store.Heartbeat(ctx, req.GetWorkerId(), id); err != nil {
		if errors.Is(err, store.ErrNotOwner) {
			return nil, status.Error(codes.FailedPrecondition, "not the current owner of this task")
		}
		return nil, status.Errorf(codes.Internal, "heartbeat: %v", err)
	}
	return &pb.HeartbeatResponse{}, nil
}

func (s *Server) AppendLog(ctx context.Context, req *pb.AppendLogRequest) (*pb.AppendLogResponse, error) {
	if req.GetWorkerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "worker_id is required")
	}
	if req.GetMessage() == "" {
		return nil, status.Error(codes.InvalidArgument, "message is required")
	}
	id, err := uuid.Parse(req.GetTaskId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "task_id must be a UUID")
	}
	entry, err := s.store.AppendTaskLog(ctx, req.GetWorkerId(), id, req.GetMessage())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, status.Errorf(codes.NotFound, "task %s not found", id)
		}
		if errors.Is(err, store.ErrNotOwner) {
			return nil, status.Error(codes.FailedPrecondition, "not the current owner of this task")
		}
		return nil, status.Errorf(codes.Internal, "append_log: %v", err)
	}
	return &pb.AppendLogResponse{Id: entry.ID}, nil
}

func (s *Server) UpdateProgress(ctx context.Context, req *pb.UpdateProgressRequest) (*pb.UpdateProgressResponse, error) {
	if req.GetWorkerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "worker_id is required")
	}
	id, err := uuid.Parse(req.GetTaskId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "task_id must be a UUID")
	}
	if err := s.store.UpdateProgress(ctx, req.GetWorkerId(), id, req.GetNote()); err != nil {
		if errors.Is(err, store.ErrNotOwner) {
			return nil, status.Error(codes.FailedPrecondition, "not the current owner of this task")
		}
		return nil, status.Errorf(codes.Internal, "update_progress: %v", err)
	}
	return &pb.UpdateProgressResponse{}, nil
}

func (s *Server) ReportResult(ctx context.Context, req *pb.ReportResultRequest) (*pb.ReportResultResponse, error) {
	id, err := uuid.Parse(req.GetTaskId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "task_id must be a UUID")
	}
	var (
		success bool
		errMsg  string
	)
	switch outcome := req.GetOutcome().(type) {
	case *pb.ReportResultRequest_Success_:
		success = true
	case *pb.ReportResultRequest_Failure_:
		success = false
		errMsg = outcome.Failure.GetMessage()
	default:
		return nil, status.Error(codes.InvalidArgument, "outcome is required (success or failure)")
	}
	terminal, err := s.store.ReportResult(ctx, req.GetWorkerId(), id, success, errMsg)
	if err != nil {
		if errors.Is(err, store.ErrNotOwner) {
			return nil, status.Error(codes.FailedPrecondition, "not the current owner of this task")
		}
		return nil, status.Errorf(codes.Internal, "report: %v", err)
	}
	switch {
	case success:
		metrics.TasksCompletedTotal.Inc()
	case terminal:
		// Worker-reported failure that exhausted attempts.
		metrics.TasksFailedTotal.WithLabelValues("reported").Inc()
	}
	// A non-terminal worker failure is a retry — don't count as a terminal
	// failure here; the reaper / next ReportResult will count it when it
	// finally lands in the failed state.
	return &pb.ReportResultResponse{}, nil
}
