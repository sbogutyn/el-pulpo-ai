package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// emitTask prints a single task either as pretty-printed JSON (when jsonOut
// is set) or as a short human summary followed by a key/value block.
func emitTask(w io.Writer, t *pb.TaskDetail, jsonOut bool, prefix string) error {
	if jsonOut {
		return encodeJSON(w, protoTaskToJSON(t))
	}
	if prefix != "" {
		fmt.Fprintln(w, prefix)
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "ID\t%s\n", t.GetId())
	fmt.Fprintf(tw, "Name\t%s\n", t.GetName())
	fmt.Fprintf(tw, "Status\t%s\n", t.GetStatus())
	fmt.Fprintf(tw, "Priority\t%d\n", t.GetPriority())
	fmt.Fprintf(tw, "Attempts\t%d / %d\n", t.GetAttemptCount(), t.GetMaxAttempts())
	if cb := t.GetClaimedBy(); cb != "" {
		fmt.Fprintf(tw, "Claimed by\t%s\n", cb)
	}
	if ts := t.GetScheduledFor(); ts != nil && ts.IsValid() {
		fmt.Fprintf(tw, "Scheduled for\t%s\n", ts.AsTime().UTC().Format(time.RFC3339))
	}
	if ts := t.GetLastHeartbeatAt(); ts != nil && ts.IsValid() {
		fmt.Fprintf(tw, "Last heartbeat\t%s\n", ts.AsTime().UTC().Format(time.RFC3339))
	}
	if ts := t.GetCompletedAt(); ts != nil && ts.IsValid() {
		fmt.Fprintf(tw, "Completed\t%s\n", ts.AsTime().UTC().Format(time.RFC3339))
	}
	if msg := t.GetLastError(); msg != "" {
		fmt.Fprintf(tw, "Last error\t%s\n", msg)
	}
	if v := t.GetJiraUrl(); v != "" {
		fmt.Fprintf(tw, "JIRA\t%s\n", v)
	}
	if v := t.GetGithubPrUrl(); v != "" {
		fmt.Fprintf(tw, "GitHub PR\t%s\n", v)
	}
	fmt.Fprintf(tw, "Created\t%s\n", t.GetCreatedAt().AsTime().UTC().Format(time.RFC3339))
	fmt.Fprintf(tw, "Updated\t%s\n", t.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339))
	if p := t.GetPayload(); len(p) > 0 {
		fmt.Fprintf(tw, "Payload\t%s\n", compactJSON(p))
	}
	return tw.Flush()
}

func renderTasksTable(w io.Writer, items []*pb.TaskDetail, total int) error {
	if len(items) == 0 {
		fmt.Fprintf(w, "(no tasks; total=%d)\n", total)
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tNAME\tPRIORITY\tATTEMPTS\tCLAIMED_BY\tUPDATED")
	for _, t := range items {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d/%d\t%s\t%s\n",
			t.GetId(), t.GetStatus(), t.GetName(), t.GetPriority(),
			t.GetAttemptCount(), t.GetMaxAttempts(),
			nonEmpty(t.GetClaimedBy()),
			t.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339),
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(w, "\n(showing %d of %d)\n", len(items), total)
	return nil
}

func protoTaskToJSON(t *pb.TaskDetail) map[string]any {
	m := map[string]any{
		"id":            t.GetId(),
		"name":          t.GetName(),
		"priority":      t.GetPriority(),
		"status":        t.GetStatus(),
		"attempt_count": t.GetAttemptCount(),
		"max_attempts":  t.GetMaxAttempts(),
		"created_at":    t.GetCreatedAt().AsTime().UTC().Format(time.RFC3339),
		"updated_at":    t.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339),
	}
	if p := t.GetPayload(); len(p) > 0 {
		var tmp any
		if err := json.Unmarshal(p, &tmp); err == nil {
			m["payload"] = tmp
		} else {
			m["payload"] = string(p)
		}
	}
	if v := t.GetClaimedBy(); v != "" {
		m["claimed_by"] = v
	}
	if v := t.GetLastError(); v != "" {
		m["last_error"] = v
	}
	if v := t.GetJiraUrl(); v != "" {
		m["jira_url"] = v
	}
	if v := t.GetGithubPrUrl(); v != "" {
		m["github_pr_url"] = v
	}
	if ts := t.GetScheduledFor(); ts != nil && ts.IsValid() {
		m["scheduled_for"] = ts.AsTime().UTC().Format(time.RFC3339)
	}
	if ts := t.GetClaimedAt(); ts != nil && ts.IsValid() {
		m["claimed_at"] = ts.AsTime().UTC().Format(time.RFC3339)
	}
	if ts := t.GetLastHeartbeatAt(); ts != nil && ts.IsValid() {
		m["last_heartbeat_at"] = ts.AsTime().UTC().Format(time.RFC3339)
	}
	if ts := t.GetCompletedAt(); ts != nil && ts.IsValid() {
		m["completed_at"] = ts.AsTime().UTC().Format(time.RFC3339)
	}
	return m
}

func protoTasksToJSON(items []*pb.TaskDetail) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, t := range items {
		out = append(out, protoTaskToJSON(t))
	}
	return out
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// formatErr turns a gRPC error into an operator-friendly shell error. It
// unwraps the gRPC status so the caller sees the original message rather
// than the noisy "rpc error: code = ... desc = ..." envelope.
func formatErr(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.Unauthenticated:
		return fmt.Errorf("mastermind rejected admin token (check ADMIN_TOKEN)")
	case codes.Unavailable:
		return fmt.Errorf("mastermind unreachable (%s)", st.Message())
	default:
		return fmt.Errorf("%s: %s", strings.ToLower(st.Code().String()), st.Message())
	}
}

func compactJSON(b []byte) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		return string(b)
	}
	return buf.String()
}

func nonEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// stdinOrEmpty and readFile exist as thin shims so tests can swap them.
var stdinOrEmpty = func() io.Reader { return os.Stdin }

var readFile = func(path string) ([]byte, error) { return os.ReadFile(path) }
