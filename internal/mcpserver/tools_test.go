package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// startMCPClient builds the MCP server wired to the provided AdminService
// client, connects a matching MCP client over an in-memory transport, and
// returns the client session.
func startMCPClient(t *testing.T, admin pb.AdminServiceClient) *mcp.ClientSession {
	t.Helper()
	serverT, clientT := mcp.NewInMemoryTransports()

	srv := NewServer(admin)
	go func() { _ = srv.Run(context.Background(), serverT) }()

	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	session, err := c.Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func TestCreateTaskTool_Happy(t *testing.T) {
	admin, _ := startAdminBuf(t)
	session := startMCPClient(t, admin)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "create_task",
		Arguments: map[string]any{"name": "build", "priority": 5},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("tool error: %+v", res.Content)
	}

	// Structured content should be JSON-decodable to TaskDetail shape.
	if res.StructuredContent == nil {
		t.Fatal("no structured content")
	}
	raw, _ := json.Marshal(res.StructuredContent)
	var out struct {
		Name     string `json:"name"`
		Priority int    `json:"priority"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode structured: %v", err)
	}
	if out.Name != "build" || out.Priority != 5 || out.Status != "pending" {
		t.Errorf("got %+v, want name=build priority=5 status=pending", out)
	}
}

func TestCreateTaskTool_MissingName_ToolError(t *testing.T) {
	admin, _ := startAdminBuf(t)
	session := startMCPClient(t, admin)

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "create_task",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool (protocol): %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for missing name")
	}
}
