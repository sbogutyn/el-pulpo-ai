//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestMain brings the compose stack up, builds the stdio MCP binary, runs
// the tests, then tears the stack down (unless E2E_KEEP=1).
func TestMain(m *testing.M) {
	if os.Getenv("E2E_SKIP_STACK") == "1" {
		// Escape hatch: assume the stack is already up (e.g. left running
		// with E2E_KEEP=1 earlier) and skip the up/build phase.
		S = mustDefaultStack(false)
		os.Exit(m.Run())
	}

	keep := os.Getenv("E2E_KEEP") == "1"

	root, err := repoRoot()
	if err != nil {
		log.Fatalf("e2e: %v", err)
	}

	S = mustDefaultStack(keep)
	S.ComposeFile = filepath.Join(root, "docker-compose.e2e.yml")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	log.Printf("e2e: bringing stack up (file=%s)", S.ComposeFile)
	if err := S.Up(ctx); err != nil {
		log.Fatalf("e2e: stack up: %v", err)
	}
	log.Printf("e2e: stack up OK; endpoints grpc=%s http=%s worker-mcp=%s",
		S.MastermindGRPC, S.MastermindHTTP, S.WorkerMCPURL)

	bin, err := BuildMastermindMCPBinary(ctx, root, filepath.Join(root, "e2e", ".bin", "mastermind-mcp"))
	if err != nil {
		// Not fatal; some tests can still run. Mark it empty so subtests
		// that need it skip.
		log.Printf("e2e: WARN: could not build mastermind-mcp: %v", err)
	} else {
		S.MastermindMCPBin = bin
		log.Printf("e2e: mastermind-mcp built at %s", bin)
	}

	code := m.Run()

	downCtx, downCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer downCancel()
	if err := S.Down(downCtx); err != nil {
		log.Printf("e2e: stack down: %v", err)
	}

	os.Exit(code)
}

func mustDefaultStack(keep bool) *Stack {
	return &Stack{
		PostgresDSN:    "postgres://pulpo:pulpo@127.0.0.1:15432/pulpo?sslmode=disable",
		MastermindGRPC: "127.0.0.1:15051",
		MastermindHTTP: "http://127.0.0.1:18080",
		WorkerMCPURL:   "http://127.0.0.1:17777",
		WorkerToken:    "e2e-worker-token",
		AdminToken:     "e2e-admin-token",
		AdminUser:      "e2e",
		AdminPassword:  "e2e",
		keep:           keep,
	}
}

// requireStack is called from every test to short-circuit if the stack did
// not come up. Gives a clearer error than nil-panicking on S.
func requireStack(t *testing.T) *Stack {
	t.Helper()
	if S == nil {
		t.Skip("e2e: stack not initialized (TestMain did not run)")
	}
	return S
}

// requireEndpointsReady confirms the basic ports are accepting traffic
// before a test proceeds. A trivial guard that hides the handful of tests
// that run before compose --wait has fully propagated.
func requireEndpointsReady(t *testing.T) {
	t.Helper()
	s := requireStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := WaitForHTTP(ctx, s.MastermindHTTP+"/healthz", 200); err != nil {
		t.Fatalf("mastermind /healthz not ready: %v", err)
	}
	if err := WaitForHTTP(ctx, s.WorkerMCPURL+"/healthz", 200); err != nil {
		t.Fatalf("worker /healthz not ready: %v", err)
	}
	if err := WaitForTCP(ctx, s.MastermindGRPC); err != nil {
		t.Fatalf("mastermind gRPC not ready: %v", err)
	}
}

// failWithLogs dumps compose logs into t.Log and then fails. Call this at
// the top of a test's cleanup if it fails, so the developer has breadcrumbs
// without manual docker commands.
func failWithLogs(t *testing.T, format string, args ...any) {
	t.Helper()
	if S != nil {
		S.DumpLogs(t, 300)
	}
	t.Fatalf(format, args...)
}

// Compile-time sanity check that our helpers are referenced; go test -tags=e2e
// would otherwise flag unused imports from files that skip conditionally.
var _ = fmt.Sprintf
