package taskclient

import (
	"context"
	"errors"
	"net"
	"sync"
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

	mu sync.Mutex

	claimTask    *pb.Task
	claimErrCode codes.Code
	claimsServed int32

	heartbeats    int32
	progressNote  string
	progressCalls int32

	reportCalls int32
	lastReport  *pb.ReportResultRequest

	failNextHeartbeat bool
}

func (f *fakeServer) ClaimTask(context.Context, *pb.ClaimTaskRequest) (*pb.ClaimTaskResponse, error) {
	if f.claimErrCode != codes.OK {
		return nil, status.Error(f.claimErrCode, "boom")
	}
	atomic.AddInt32(&f.claimsServed, 1)
	return &pb.ClaimTaskResponse{Task: f.claimTask}, nil
}

func (f *fakeServer) Heartbeat(context.Context, *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	atomic.AddInt32(&f.heartbeats, 1)
	f.mu.Lock()
	fail := f.failNextHeartbeat
	f.failNextHeartbeat = false
	f.mu.Unlock()
	if fail {
		return nil, status.Error(codes.FailedPrecondition, "lease lost")
	}
	return &pb.HeartbeatResponse{}, nil
}

func (f *fakeServer) UpdateProgress(_ context.Context, req *pb.UpdateProgressRequest) (*pb.UpdateProgressResponse, error) {
	atomic.AddInt32(&f.progressCalls, 1)
	f.mu.Lock()
	f.progressNote = req.GetNote()
	f.mu.Unlock()
	return &pb.UpdateProgressResponse{}, nil
}

func (f *fakeServer) ReportResult(_ context.Context, req *pb.ReportResultRequest) (*pb.ReportResultResponse, error) {
	atomic.AddInt32(&f.reportCalls, 1)
	f.mu.Lock()
	f.lastReport = req
	f.mu.Unlock()
	return &pb.ReportResultResponse{}, nil
}

func dialFake(t *testing.T, fs *fakeServer) pb.TaskServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterTaskServiceServer(srv, fs)
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
	return pb.NewTaskServiceClient(conn)
}

func TestClient_Claim_ReturnsErrNoTaskOnNotFound(t *testing.T) {
	fs := &fakeServer{claimErrCode: codes.NotFound}
	c := NewClient(dialFake(t, fs), "w1")
	_, err := c.Claim(context.Background())
	if !errors.Is(err, ErrNoTask) {
		t.Errorf("got %v, want ErrNoTask", err)
	}
}

func TestClient_Claim_ReturnsTaskOnSuccess(t *testing.T) {
	fs := &fakeServer{claimTask: &pb.Task{Id: "id-1", Name: "name-1", Payload: []byte(`{"k":1}`)}}
	c := NewClient(dialFake(t, fs), "w1")
	task, err := c.Claim(context.Background())
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if task.ID() != "id-1" || task.Name() != "name-1" || string(task.Payload()) != `{"k":1}` {
		t.Errorf("unexpected task %+v", task)
	}
}

func TestTask_ProgressSendsNote(t *testing.T) {
	fs := &fakeServer{claimTask: &pb.Task{Id: "id", Name: "n"}}
	c := NewClient(dialFake(t, fs), "w1")
	task, _ := c.Claim(context.Background())
	if err := task.Progress(context.Background(), "hello"); err != nil {
		t.Fatalf("Progress: %v", err)
	}
	fs.mu.Lock()
	note := fs.progressNote
	fs.mu.Unlock()
	if note != "hello" {
		t.Errorf("note=%q, want hello", note)
	}
}

func TestTask_CompleteSendsSuccess(t *testing.T) {
	fs := &fakeServer{claimTask: &pb.Task{Id: "id", Name: "n"}}
	c := NewClient(dialFake(t, fs), "w1")
	task, _ := c.Claim(context.Background())
	if err := task.Complete(context.Background()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if _, ok := fs.lastReport.GetOutcome().(*pb.ReportResultRequest_Success_); !ok {
		t.Errorf("outcome=%T, want Success", fs.lastReport.GetOutcome())
	}
}

func TestTask_FailSendsFailureMessage(t *testing.T) {
	fs := &fakeServer{claimTask: &pb.Task{Id: "id", Name: "n"}}
	c := NewClient(dialFake(t, fs), "w1")
	task, _ := c.Claim(context.Background())
	if err := task.Fail(context.Background(), "oops"); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	f, ok := fs.lastReport.GetOutcome().(*pb.ReportResultRequest_Failure_)
	if !ok {
		t.Fatalf("outcome=%T, want Failure", fs.lastReport.GetOutcome())
	}
	if f.Failure.GetMessage() != "oops" {
		t.Errorf("msg=%q, want oops", f.Failure.GetMessage())
	}
}

func TestTask_DoubleFinalizeReturnsErr(t *testing.T) {
	fs := &fakeServer{claimTask: &pb.Task{Id: "id", Name: "n"}}
	c := NewClient(dialFake(t, fs), "w1")
	task, _ := c.Claim(context.Background())
	if err := task.Complete(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := task.Complete(context.Background()); !errors.Is(err, ErrAlreadyFinalized) {
		t.Errorf("second Complete err=%v, want ErrAlreadyFinalized", err)
	}
	if err := task.Fail(context.Background(), "x"); !errors.Is(err, ErrAlreadyFinalized) {
		t.Errorf("post-Complete Fail err=%v, want ErrAlreadyFinalized", err)
	}
	if got := atomic.LoadInt32(&fs.reportCalls); got != 1 {
		t.Errorf("reportCalls=%d, want 1", got)
	}
}

func TestTask_StartHeartbeatTicks(t *testing.T) {
	fs := &fakeServer{claimTask: &pb.Task{Id: "id", Name: "n"}}
	c := NewClient(dialFake(t, fs), "w1")
	task, _ := c.Claim(context.Background())

	stop := task.StartHeartbeat(context.Background(), 5*time.Millisecond, nil)
	time.Sleep(40 * time.Millisecond)
	stop()

	if got := atomic.LoadInt32(&fs.heartbeats); got < 2 {
		t.Errorf("heartbeats=%d, want >= 2", got)
	}
}

func TestTask_CompleteStopsAutoHeartbeat(t *testing.T) {
	fs := &fakeServer{claimTask: &pb.Task{Id: "id", Name: "n"}}
	c := NewClient(dialFake(t, fs), "w1")
	task, _ := c.Claim(context.Background())

	_ = task.StartHeartbeat(context.Background(), 5*time.Millisecond, nil)
	time.Sleep(20 * time.Millisecond)
	if err := task.Complete(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := atomic.LoadInt32(&fs.heartbeats)
	time.Sleep(30 * time.Millisecond)
	after := atomic.LoadInt32(&fs.heartbeats)
	if after != before {
		t.Errorf("heartbeats kept ticking after Complete: before=%d after=%d", before, after)
	}
}

func TestTask_HeartbeatErrorInvokesOnError(t *testing.T) {
	fs := &fakeServer{claimTask: &pb.Task{Id: "id", Name: "n"}}
	fs.failNextHeartbeat = true
	c := NewClient(dialFake(t, fs), "w1")
	task, _ := c.Claim(context.Background())

	errCh := make(chan error, 1)
	stop := task.StartHeartbeat(context.Background(), 5*time.Millisecond, func(e error) {
		select {
		case errCh <- e:
		default:
		}
	})
	defer stop()

	select {
	case err := <-errCh:
		if status.Code(err) != codes.FailedPrecondition {
			t.Errorf("err=%v, want FailedPrecondition", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("onError not invoked")
	}
}
