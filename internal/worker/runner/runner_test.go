package runner

import (
	"context"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

type fakeServer struct {
	pb.UnimplementedTaskServiceServer
	claimCalls  int32
	reportCalls int32
	taskToGive  string
}

func (s *fakeServer) ClaimTask(ctx context.Context, _ *pb.ClaimTaskRequest) (*pb.ClaimTaskResponse, error) {
	n := atomic.AddInt32(&s.claimCalls, 1)
	if n == 1 {
		return &pb.ClaimTaskResponse{Task: &pb.Task{Id: s.taskToGive, Name: "t", Payload: []byte("{}")}}, nil
	}
	return nil, status.Error(codes.NotFound, "no tasks")
}
func (s *fakeServer) Heartbeat(context.Context, *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	return &pb.HeartbeatResponse{}, nil
}
func (s *fakeServer) ReportResult(context.Context, *pb.ReportResultRequest) (*pb.ReportResultResponse, error) {
	atomic.AddInt32(&s.reportCalls, 1)
	return &pb.ReportResultResponse{}, nil
}

func TestRunner_ClaimsWorksAndReports(t *testing.T) {
	fs := &fakeServer{taskToGive: "11111111-1111-1111-1111-111111111111"}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterTaskServiceServer(srv, fs)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg := Config{
		WorkerID:          "test-worker",
		PollInterval:      5 * time.Millisecond,
		HeartbeatInterval: 5 * time.Millisecond,
		WorkDuration:      20 * time.Millisecond,
	}
	r := New(pb.NewTaskServiceClient(conn), cfg, log)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r.Run(ctx)

	if atomic.LoadInt32(&fs.reportCalls) == 0 {
		t.Errorf("expected at least one ReportResult call")
	}
}
