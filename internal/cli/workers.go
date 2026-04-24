package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

func runWorkers(ctx context.Context, cfg Config, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: elpulpo workers list")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return runWorkersList(ctx, cfg, rest, stdout, stderr)
	default:
		return fmt.Errorf("unknown workers subcommand %q", sub)
	}
}

func runWorkersList(ctx context.Context, cfg Config, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("workers list", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit raw JSON rather than a table")
	if _, err := parseFlags(fs, stderr, args); err != nil {
		return err
	}
	client, closer, err := newAdminClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer closer.Close()

	ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()

	resp, err := client.ListWorkers(ctx, &pb.ListWorkersRequest{})
	if err != nil {
		return formatErr(err)
	}
	if *jsonOut {
		return encodeJSON(stdout, map[string]any{
			"items": protoWorkersToJSON(resp.GetItems()),
		})
	}
	return renderWorkersTable(stdout, resp.GetItems())
}

func renderWorkersTable(w io.Writer, items []*pb.WorkerInfo) error {
	if len(items) == 0 {
		fmt.Fprintln(w, "(no workers have ever claimed a task)")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "WORKER_ID\tACTIVE\tCOMPLETED\tFAILED\tLAST_SEEN")
	for _, it := range items {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%s\n",
			it.GetId(), it.GetActiveTasks(), it.GetCompletedTasks(), it.GetFailedTasks(),
			formatTimestamp(it.GetLastSeenAt()))
	}
	return tw.Flush()
}

func formatTimestamp(ts interface {
	IsValid() bool
	AsTime() time.Time
}) string {
	if ts == nil || !ts.IsValid() {
		return "-"
	}
	return ts.AsTime().UTC().Format(time.RFC3339)
}

func protoWorkersToJSON(items []*pb.WorkerInfo) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, it := range items {
		m := map[string]any{
			"id":              it.GetId(),
			"active_tasks":    it.GetActiveTasks(),
			"completed_tasks": it.GetCompletedTasks(),
			"failed_tasks":    it.GetFailedTasks(),
		}
		if ts := it.GetLastSeenAt(); ts != nil && ts.IsValid() {
			m["last_seen_at"] = ts.AsTime().UTC().Format(time.RFC3339)
		}
		out = append(out, m)
	}
	return out
}
