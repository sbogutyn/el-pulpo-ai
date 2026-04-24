package grpcserver

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

func startBufServer(t *testing.T) (pb.TaskServiceClient, *store.Store) {
	t.Helper()
	s, err := store.Open(context.Background(), testDSN)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(s.Close)
	_, err = s.Pool().Exec(context.Background(), "TRUNCATE TABLE tasks")
	if err != nil {
		t.Fatal(err)
	}

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterTaskServiceServer(srv, New(s))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return pb.NewTaskServiceClient(conn), s
}

func TestClaimTask_ReturnsNotFoundOnEmptyQueue(t *testing.T) {
	client, _ := startBufServer(t)
	_, err := client.ClaimTask(context.Background(), &pb.ClaimTaskRequest{WorkerId: "w"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("code=%v, want NotFound", status.Code(err))
	}
}

func TestClaimThenReport_Success(t *testing.T) {
	client, s := startBufServer(t)
	ctx := context.Background()

	if _, err := s.CreateTask(ctx, store.NewTaskInput{Name: "t", MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	resp, err := client.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	taskID := resp.Task.Id

	if _, err := client.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: "w1", TaskId: taskID}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if _, err := client.ReportResult(ctx, &pb.ReportResultRequest{
		WorkerId: "w1", TaskId: taskID,
		Outcome: &pb.ReportResultRequest_Success_{Success: &pb.ReportResultRequest_Success{}},
	}); err != nil {
		t.Fatalf("ReportResult: %v", err)
	}
}

func TestHeartbeat_WrongOwner_FailsPrecondition(t *testing.T) {
	client, s := startBufServer(t)
	ctx := context.Background()
	if _, err := s.CreateTask(ctx, store.NewTaskInput{Name: "t", MaxAttempts: 3}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	resp, err := client.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	_, err = client.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: "other", TaskId: resp.Task.Id})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code=%v, want FailedPrecondition", status.Code(err))
	}
}

func TestUpdateProgress_StoresNote(t *testing.T) {
	client, s := startBufServer(t)
	ctx := context.Background()
	if _, err := s.CreateTask(ctx, store.NewTaskInput{Name: "t", MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	resp, err := client.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if _, err := client.UpdateProgress(ctx, &pb.UpdateProgressRequest{
		WorkerId: "w1", TaskId: resp.Task.Id, Note: "half done",
	}); err != nil {
		t.Fatalf("UpdateProgress: %v", err)
	}
}

func TestUpdateProgress_WrongOwner_FailsPrecondition(t *testing.T) {
	client, s := startBufServer(t)
	ctx := context.Background()
	if _, err := s.CreateTask(ctx, store.NewTaskInput{Name: "t", MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	resp, err := client.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w1"})
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	_, err = client.UpdateProgress(ctx, &pb.UpdateProgressRequest{
		WorkerId: "other", TaskId: resp.Task.Id, Note: "n",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("code=%v, want FailedPrecondition", status.Code(err))
	}
}

func TestUpdateProgress_InvalidTaskID_FailsInvalidArgument(t *testing.T) {
	client, _ := startBufServer(t)
	_, err := client.UpdateProgress(context.Background(), &pb.UpdateProgressRequest{
		WorkerId: "w", TaskId: "not-a-uuid", Note: "n",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code=%v, want InvalidArgument", status.Code(err))
	}
}

func TestClaimTask_WithDeadline_StillReturnsNotFound(t *testing.T) {
	client, _ := startBufServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := client.ClaimTask(ctx, &pb.ClaimTaskRequest{WorkerId: "w"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("code=%v, want NotFound", status.Code(err))
	}
}
