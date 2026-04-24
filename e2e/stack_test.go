//go:build e2e

// Package e2e is the end-to-end test harness for el-pulpo. It stands up a
// production-like stack (mastermind + worker + postgres) via docker-compose,
// then drives every externally-observable feature — gRPC, admin HTTP UI,
// worker MCP, and the mastermind-mcp stdio binary — with the real clients a
// caller would use in production.
//
// See docs/superpowers/specs/2026-04-24-e2e-testing-design.md for the spec.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Stack holds everything a test needs to reach the running services. It is
// populated once in TestMain and handed to each test via [S].
type Stack struct {
	ComposeFile string

	// Host-mapped endpoints.
	PostgresDSN    string // for raw SQL probes
	MastermindGRPC string // "127.0.0.1:15051"
	MastermindHTTP string // "http://127.0.0.1:18080"
	WorkerMCPURL   string // "http://127.0.0.1:17777"

	// Shared secrets — must match docker-compose.e2e.yml.
	WorkerToken   string
	AdminToken    string
	AdminUser     string
	AdminPassword string

	// Path to the mastermind-mcp binary built for the host. The stdio
	// transport requires a local executable, so we compile it once in
	// TestMain.
	MastermindMCPBin string

	// keep controls whether [Stack.Down] is a no-op. Set from E2E_KEEP=1.
	keep bool
}

// S is the stack built by TestMain and shared by every test. Kept as a
// package-level var because testing.T isn't threaded through TestMain
// setup.
var S *Stack

// Up brings the docker-compose stack to a running state, then blocks until
// mastermind /readyz and worker /healthz both answer 200 from the host.
//
// Why not `docker compose --wait`: mastermind and worker run on distroless
// images that have no shell, curl, or wget — there is no usable in-container
// probe command, and `--wait` errors out on a service with no healthcheck.
// Polling from the host achieves the same guarantee without requiring the
// runtime images to carry a shell.
func (s *Stack) Up(ctx context.Context) error {
	if err := hasDocker(); err != nil {
		return err
	}
	args := []string{"compose", "-f", s.ComposeFile, "up", "-d", "--build"}
	if err := runCmd(ctx, "docker", args...); err != nil {
		return err
	}
	readyCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if err := WaitForHTTP(readyCtx, s.MastermindHTTP+"/readyz", http.StatusOK); err != nil {
		return fmt.Errorf("mastermind /readyz: %w", err)
	}
	if err := WaitForHTTP(readyCtx, s.WorkerMCPURL+"/healthz", http.StatusOK); err != nil {
		return fmt.Errorf("worker /healthz: %w", err)
	}
	if err := WaitForTCP(readyCtx, s.MastermindGRPC); err != nil {
		return fmt.Errorf("mastermind gRPC: %w", err)
	}
	return nil
}

// Down tears the stack down and removes volumes. Honors E2E_KEEP=1 by
// returning nil without doing anything, so a developer can poke the stack
// after a test run.
func (s *Stack) Down(ctx context.Context) error {
	if s.keep {
		log.Printf("e2e: E2E_KEEP=1, leaving stack up (tear down manually with: docker compose -f %s down -v)", s.ComposeFile)
		return nil
	}
	return runCmd(ctx, "docker", "compose", "-f", s.ComposeFile, "down", "-v")
}

// DumpLogs prints the last N lines of each service's logs through t.Log.
// Useful in test teardown when something went wrong.
func (s *Stack) DumpLogs(t *testing.T, tail int) {
	t.Helper()
	args := []string{"compose", "-f", s.ComposeFile, "logs", "--no-color", fmt.Sprintf("--tail=%d", tail)}
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Logf("e2e: docker compose logs failed: %v", err)
	}
	t.Logf("e2e: --- docker compose logs (tail=%d) ---\n%s", tail, out)
}

// WaitForHTTP polls a URL until it returns the expected status code or the
// ctx deadline fires. Used as a belt-and-braces guard after compose --wait,
// when a test needs an endpoint to be not just healthy but definitely
// serving.
func WaitForHTTP(ctx context.Context, url string, want int) error {
	c := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	for time.Now().Before(deadline) {
		resp, err := c.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == want {
				return nil
			}
			lastErr = fmt.Errorf("status %d, want %d", resp.StatusCode, want)
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("WaitForHTTP(%s): %w", url, lastErr)
}

// WaitForTCP polls a TCP endpoint until Dial succeeds or ctx fires. Used
// for the gRPC port, which doesn't speak HTTP but must be accepting
// connections.
func WaitForTCP(ctx context.Context, addr string) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	var lastErr error
	for time.Now().Before(deadline) {
		d := net.Dialer{Timeout: 2 * time.Second}
		c, err := d.DialContext(ctx, "tcp", addr)
		if err == nil {
			c.Close()
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return fmt.Errorf("WaitForTCP(%s): %w", addr, lastErr)
}

// BuildMastermindMCPBinary builds the stdio MCP binary for the host so the
// `mcp.CommandTransport` can spawn it. Writing to the provided out path;
// returns the absolute path written.
func BuildMastermindMCPBinary(ctx context.Context, repoRoot, out string) (string, error) {
	abs, err := filepath.Abs(out)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-o", abs, "./cmd/mastermind-mcp")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out_, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("build mastermind-mcp: %w\n%s", err, out_)
	}
	return abs, nil
}

// repoRoot walks up from this source file until it finds go.mod.
func repoRoot() (string, error) {
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("repoRoot: go.mod not found starting from %s", thisFile)
}

func hasDocker() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker not found on PATH (E2E suite requires Docker): %w", err)
	}
	out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").Output()
	if err != nil {
		return fmt.Errorf("docker daemon not reachable: %w", err)
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return fmt.Errorf("docker info returned empty output")
	}
	return nil
}

// runCmd runs a command with output streamed through log.Printf. Keeps the
// test-run console informative without duplicating docker's output into
// test failure messages (tests use DumpLogs for that).
func runCmd(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var mu sync.Mutex
	tee := &lineWriter{prefix: fmt.Sprintf("[%s]", name), mu: &mu}
	cmd.Stdout = tee
	cmd.Stderr = tee
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// lineWriter logs each complete line with a prefix so compose output is
// distinguishable from Go test output.
type lineWriter struct {
	prefix string
	buf    bytes.Buffer
	mu     *sync.Mutex
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	for {
		idx := bytes.IndexByte(w.buf.Bytes(), '\n')
		if idx < 0 {
			break
		}
		line := string(w.buf.Bytes()[:idx])
		w.buf.Next(idx + 1)
		log.Printf("%s %s", w.prefix, line)
	}
	return n, err
}

// Assertion helper used across tests.
func eventually(ctx context.Context, interval time.Duration, check func() error) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	var lastErr error
	for time.Now().Before(deadline) {
		if err := check(); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("eventually: deadline with no error recorded")
	}
	return fmt.Errorf("eventually: %w", lastErr)
}

// readBodyLimited reads up to n bytes of an HTTP response body, preserving
// the original error if any. Keeps test logs readable.
func readBodyLimited(r io.Reader, n int64) []byte {
	b, _ := io.ReadAll(io.LimitReader(r, n))
	return b
}
