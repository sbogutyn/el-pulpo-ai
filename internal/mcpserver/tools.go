package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// TaskDetail is the JSON shape the MCP tools return. Field tags use
// snake_case to match the MCP convention; optional fields use `omitempty` so
// an unclaimed task doesn't carry empty claim metadata.
type TaskDetail struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	Payload         any        `json:"payload"`
	Priority        int32      `json:"priority"`
	Status          string     `json:"status"`
	ScheduledFor    *time.Time `json:"scheduled_for,omitempty"`
	AttemptCount    int32      `json:"attempt_count"`
	MaxAttempts     int32      `json:"max_attempts"`
	ClaimedBy       string     `json:"claimed_by,omitempty"`
	ClaimedAt       *time.Time `json:"claimed_at,omitempty"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	JiraURL         string     `json:"jira_url,omitempty"`
	GithubPRURL     string     `json:"github_pr_url,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func fromProtoTask(p *pb.TaskDetail) TaskDetail {
	d := TaskDetail{
		ID:           p.GetId(),
		Name:         p.GetName(),
		Priority:     p.GetPriority(),
		Status:       p.GetStatus(),
		AttemptCount: p.GetAttemptCount(),
		MaxAttempts:  p.GetMaxAttempts(),
		ClaimedBy:    p.GetClaimedBy(),
		LastError:    p.GetLastError(),
		JiraURL:      p.GetJiraUrl(),
		GithubPRURL:  p.GetGithubPrUrl(),
		CreatedAt:    p.GetCreatedAt().AsTime(),
		UpdatedAt:    p.GetUpdatedAt().AsTime(),
	}
	// Decode raw JSON payload into a generic value so the MCP output schema
	// (inferred from TaskDetail) doesn't trip on `[]byte`-typed fields; an
	// empty or invalid payload becomes an empty object.
	raw := p.GetPayload()
	if len(raw) == 0 {
		d.Payload = map[string]any{}
	} else if err := json.Unmarshal(raw, &d.Payload); err != nil {
		d.Payload = map[string]any{}
	}
	if t := p.GetScheduledFor(); t.IsValid() {
		tt := t.AsTime()
		d.ScheduledFor = &tt
	}
	if t := p.GetClaimedAt(); t.IsValid() {
		tt := t.AsTime()
		d.ClaimedAt = &tt
	}
	if t := p.GetLastHeartbeatAt(); t.IsValid() {
		tt := t.AsTime()
		d.LastHeartbeatAt = &tt
	}
	if t := p.GetCompletedAt(); t.IsValid() {
		tt := t.AsTime()
		d.CompletedAt = &tt
	}
	return d
}

// CreateTaskInput is the MCP tool input for create_task. The SDK derives the
// JSON schema from these struct tags.
type CreateTaskInput struct {
	Name         string          `json:"name" jsonschema:"the task type (required), 1-200 chars"`
	Payload      json.RawMessage `json:"payload,omitempty" jsonschema:"opaque JSON payload, default {}"`
	Priority     int32           `json:"priority,omitempty" jsonschema:"priority, default 0 (higher runs first)"`
	MaxAttempts  int32           `json:"max_attempts,omitempty" jsonschema:"max attempts, default 3, range 1-50"`
	ScheduledFor *time.Time      `json:"scheduled_for,omitempty" jsonschema:"earliest time the task is eligible to run (RFC3339)"`
	JiraURL      string          `json:"jira_url,omitempty" jsonschema:"optional JIRA issue URL"`
	GithubPRURL  string          `json:"github_pr_url,omitempty" jsonschema:"optional GitHub pull request URL"`
}

func registerCreateTask(s *mcp.Server, admin pb.AdminServiceClient) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_task",
		Description: "Create a new task in the mastermind queue. Returns the created task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in CreateTaskInput) (*mcp.CallToolResult, TaskDetail, error) {
		req := &pb.CreateTaskRequest{
			Name:        in.Name,
			Payload:     []byte(in.Payload),
			Priority:    in.Priority,
			MaxAttempts: in.MaxAttempts,
			JiraUrl:     in.JiraURL,
			GithubPrUrl: in.GithubPRURL,
		}
		if in.ScheduledFor != nil {
			req.ScheduledFor = timestamppb.New(*in.ScheduledFor)
		}
		resp, err := admin.CreateTask(ctx, req)
		if err != nil {
			return toolErr(err, "create_task"), TaskDetail{}, nil
		}
		d := fromProtoTask(resp.GetTask())
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{
				Text: fmt.Sprintf("Created task %s (%s)", d.ID, d.Name),
			}},
		}, d, nil
	})
}

// toolErr converts a gRPC error from mastermind into an MCP tool error.
// We always return tool errors (IsError=true) rather than protocol errors —
// the MCP server itself should never fail a call just because an RPC didn't.
func toolErr(err error, tool string) *mcp.CallToolResult {
	st, _ := status.FromError(err)
	var msg string
	switch st.Code() {
	case codes.InvalidArgument, codes.NotFound:
		msg = st.Message()
	case codes.Unauthenticated:
		msg = "mastermind rejected admin token"
	case codes.Unavailable:
		msg = "mastermind unreachable"
	default:
		msg = "internal error"
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s: %s", tool, msg)}},
	}
}
