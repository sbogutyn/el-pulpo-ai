// Package grpcserver implements the gRPC TaskService handlers.
package grpcserver

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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
	t, err := s.store.ClaimTask(ctx, req.GetWorkerId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "claim: %v", err)
	}
	if t == nil {
		return nil, status.Error(codes.NotFound, "no tasks available")
	}
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
	if err := s.store.ReportResult(ctx, req.GetWorkerId(), id, success, errMsg); err != nil {
		if errors.Is(err, store.ErrNotOwner) {
			return nil, status.Error(codes.FailedPrecondition, "not the current owner of this task")
		}
		return nil, status.Errorf(codes.Internal, "report: %v", err)
	}
	return &pb.ReportResultResponse{}, nil
}
