//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// dialGRPC builds a gRPC client connection to mastermind with the given
// bearer token. The test owns the returned conn; t.Cleanup closes it.
func dialGRPC(t *testing.T, token string) *grpc.ClientConn {
	t.Helper()
	s := requireStack(t)
	conn, err := grpc.NewClient(s.MastermindGRPC,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.BearerCredentials(token)),
	)
	if err != nil {
		t.Fatalf("grpc dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// workerClient returns a pb.TaskServiceClient authenticated with
// WORKER_TOKEN.
func workerClient(t *testing.T) pb.TaskServiceClient {
	t.Helper()
	return pb.NewTaskServiceClient(dialGRPC(t, requireStack(t).WorkerToken))
}

// adminClient returns a pb.AdminServiceClient authenticated with
// ADMIN_TOKEN.
func adminClient(t *testing.T) pb.AdminServiceClient {
	t.Helper()
	return pb.NewAdminServiceClient(dialGRPC(t, requireStack(t).AdminToken))
}

// httpRequest builds an HTTP request against the mastermind admin UI with
// optional basic auth. `path` must begin with `/`. A non-empty `form` is
// sent as application/x-www-form-urlencoded.
func httpRequest(t *testing.T, method, path string, form url.Values, withAuth bool) *http.Response {
	t.Helper()
	s := requireStack(t)
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, s.MastermindHTTP+path, body)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if withAuth {
		req.SetBasicAuth(s.AdminUser, s.AdminPassword)
	}
	c := &http.Client{
		Timeout: 10 * time.Second,
		// Don't auto-follow redirects — the handlers use 303 See Other on
		// create/update/delete and the tests want to inspect the Location
		// header rather than the final page.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("http %s %s: %v", method, path, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// httpGetAuth is a convenience wrapper over httpRequest for
// authenticated GETs.
func httpGetAuth(t *testing.T, path string) *http.Response {
	t.Helper()
	return httpRequest(t, http.MethodGet, path, nil, true)
}

// connectWorkerMCP opens an MCP client session against the worker's
// streamable HTTP endpoint. The session is closed via t.Cleanup so
// finalization races don't leak claims into later tests.
func connectWorkerMCP(t *testing.T) *mcp.ClientSession {
	t.Helper()
	s := requireStack(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e-worker-client", Version: "v1"}, nil)
	sess, err := c.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:             s.WorkerMCPURL + "/mcp",
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect worker MCP: %v", err)
	}
	t.Cleanup(func() {
		cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer ccancel()
		_ = sess.Close()
		_ = cctx // reserved; sess.Close is non-context for now
	})
	return sess
}

// connectMastermindMCP spawns the mastermind-mcp stdio binary with the
// given token and opens an MCP client session against it.
func connectMastermindMCP(t *testing.T, adminToken string) *mcp.ClientSession {
	t.Helper()
	s := requireStack(t)
	if s.MastermindMCPBin == "" {
		t.Skip("mastermind-mcp binary not built in TestMain; skipping stdio tests")
	}
	cmd := exec.Command(s.MastermindMCPBin,
		"--addr", s.MastermindGRPC,
		"--token", adminToken,
		"--log-format", "text",
		"--log-level", "warn",
	)
	// The MCP SDK's CommandTransport wires stdio pipes itself; don't
	// pre-attach anything that closes them.
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e-mm-mcp-client", Version: "v1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sess, err := c.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect mastermind-mcp: %v", err)
	}
	t.Cleanup(func() {
		_ = sess.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	return sess
}

// callTool invokes an MCP tool by name with the given arguments (any value
// that marshals to JSON). The test fails if the JSON-RPC layer errors;
// tool-level errors (IsError=true) surface through the returned result.
func callTool(t *testing.T, sess *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

// toolText extracts the first TextContent body from a tool result, or "" if
// there is none.
func toolText(res *mcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			return tc.Text
		}
	}
	return ""
}

// probeMMCPWithToken runs the mastermind-mcp binary with the given token
// and reports whether the startup probe succeeded. Used by negative-auth
// tests to assert that a wrong token makes the binary exit rather than
// proceed to the MCP handshake.
func probeMMCPWithToken(ctx context.Context, token string) error {
	s := S
	if s == nil || s.MastermindMCPBin == "" {
		return fmt.Errorf("mastermind-mcp binary not built")
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, s.MastermindMCPBin,
		"--addr", s.MastermindGRPC,
		"--token", token,
		"--log-format", "text",
		"--log-level", "error",
	)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mastermind-mcp exited: %w (output: %s)", err, out)
	}
	return nil
}

// decodeStructured decodes the structured content of a tool result into
// `out` (typically a pointer to a struct). Returns a helpful error when
// the result is empty or was marked IsError.
func decodeStructured(res *mcp.CallToolResult, out any) error {
	if res == nil {
		return fmt.Errorf("decodeStructured: nil result")
	}
	if res.IsError {
		return fmt.Errorf("tool error: %s", toolText(res))
	}
	if res.StructuredContent == nil {
		return fmt.Errorf("decodeStructured: no structured content")
	}
	// Re-marshal the structured content to JSON and decode into `out` so
	// we don't need to know the SDK's concrete wrapper type.
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return fmt.Errorf("marshal structured: %w", err)
	}
	return json.Unmarshal(b, out)
}
