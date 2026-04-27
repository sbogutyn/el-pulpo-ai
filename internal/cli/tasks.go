package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

func runTasks(ctx context.Context, cfg Config, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: elpulpo tasks {create|get|list|cancel|retry} ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return runTasksCreate(ctx, cfg, rest, stdout, stderr)
	case "get":
		return runTasksGet(ctx, cfg, rest, stdout, stderr)
	case "list":
		return runTasksList(ctx, cfg, rest, stdout, stderr)
	case "cancel":
		return runTasksCancel(ctx, cfg, rest, stdout, stderr)
	case "retry":
		return runTasksRetry(ctx, cfg, rest, stdout, stderr)
	default:
		return fmt.Errorf("unknown tasks subcommand %q", sub)
	}
}

func runTasksCreate(ctx context.Context, cfg Config, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tasks create", flag.ContinueOnError)
	var (
		name         = fs.String("name", "", "task type (required)")
		payload      = fs.String("payload", "", "opaque JSON payload; @path reads from file, - reads stdin")
		priority     = fs.Int("priority", 0, "priority (higher runs first)")
		maxAttempts  = fs.Int("max-attempts", 0, "max attempts (default 3, 1..50)")
		scheduledFor = fs.String("scheduled-for", "", "earliest eligible time (RFC3339)")
		jiraURL      = fs.String("jira-url", "", "JIRA issue URL")
		githubPRURL  = fs.String("github-pr-url", "", "GitHub pull request URL")
		jsonOut      = fs.Bool("json", false, "emit raw TaskDetail JSON instead of human summary")
	)
	if _, err := parseFlags(fs, stderr, args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required")
	}
	req := &pb.CreateTaskRequest{
		Name:        *name,
		Priority:    int32(*priority),
		MaxAttempts: int32(*maxAttempts),
		JiraUrl:     *jiraURL,
		GithubPrUrl: *githubPRURL,
	}
	if *payload != "" {
		raw, err := readPayload(*payload, stderr)
		if err != nil {
			return err
		}
		var tmp any
		if err := json.Unmarshal(raw, &tmp); err != nil {
			return fmt.Errorf("--payload is not valid JSON: %w", err)
		}
		req.Payload = raw
	}
	if *scheduledFor != "" {
		t, err := time.Parse(time.RFC3339, *scheduledFor)
		if err != nil {
			return fmt.Errorf("--scheduled-for must be RFC3339: %w", err)
		}
		req.ScheduledFor = timestamppb.New(t)
	}

	client, closer, err := newAdminClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer closer.Close()

	ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()

	resp, err := client.CreateTask(ctx, req)
	if err != nil {
		return formatErr(err)
	}
	return emitTask(stdout, resp.GetTask(), *jsonOut, fmt.Sprintf("created task %s (%s)", resp.GetTask().GetId(), resp.GetTask().GetName()))
}

func runTasksGet(ctx context.Context, cfg Config, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tasks get", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit raw TaskDetail JSON instead of human summary")
	positional, err := parseFlags(fs, stderr, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: elpulpo tasks get <id>")
	}
	client, closer, err := newAdminClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer closer.Close()

	ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()

	resp, err := client.GetTask(ctx, &pb.GetTaskRequest{Id: positional[0]})
	if err != nil {
		return formatErr(err)
	}
	return emitTask(stdout, resp.GetTask(), *jsonOut, "")
}

func runTasksList(ctx context.Context, cfg Config, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tasks list", flag.ContinueOnError)
	var (
		statusF = fs.String("status", "", "filter: pending|claimed|in_progress|pr_opened|review_requested|completed|failed")
		limit   = fs.Int("limit", 50, "page size (1..500)")
		offset  = fs.Int("offset", 0, "pagination offset")
		jsonOut = fs.Bool("json", false, "emit raw JSON rather than a table")
	)
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

	resp, err := client.ListTasks(ctx, &pb.ListTasksRequest{
		Status: *statusF,
		Limit:  int32(*limit),
		Offset: int32(*offset),
	})
	if err != nil {
		return formatErr(err)
	}
	if *jsonOut {
		return encodeJSON(stdout, map[string]any{
			"items": protoTasksToJSON(resp.GetItems()),
			"total": resp.GetTotal(),
		})
	}
	return renderTasksTable(stdout, resp.GetItems(), int(resp.GetTotal()))
}

func runTasksCancel(ctx context.Context, cfg Config, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tasks cancel", flag.ContinueOnError)
	positional, err := parseFlags(fs, stderr, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: elpulpo tasks cancel <id>")
	}
	id := positional[0]
	client, closer, err := newAdminClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer closer.Close()

	ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()

	if _, err := client.CancelTask(ctx, &pb.CancelTaskRequest{Id: id}); err != nil {
		return formatErr(err)
	}
	fmt.Fprintf(stdout, "cancelled task %s\n", id)
	return nil
}

func runTasksRetry(ctx context.Context, cfg Config, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("tasks retry", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit raw TaskDetail JSON instead of human summary")
	positional, err := parseFlags(fs, stderr, args)
	if err != nil {
		return err
	}
	if len(positional) != 1 {
		return errors.New("usage: elpulpo tasks retry <id>")
	}
	id := positional[0]
	client, closer, err := newAdminClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer closer.Close()

	ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	defer cancel()

	resp, err := client.RetryTask(ctx, &pb.RetryTaskRequest{Id: id})
	if err != nil {
		return formatErr(err)
	}
	return emitTask(stdout, resp.GetTask(), *jsonOut, fmt.Sprintf("requeued task %s", id))
}

// readPayload resolves the --payload argument. Supports inline JSON, @file,
// and - (stdin).
func readPayload(v string, _ io.Writer) ([]byte, error) {
	switch {
	case v == "-":
		b, err := io.ReadAll(stdinOrEmpty())
		if err != nil {
			return nil, fmt.Errorf("read payload from stdin: %w", err)
		}
		return b, nil
	case strings.HasPrefix(v, "@"):
		b, err := readFile(v[1:])
		if err != nil {
			return nil, fmt.Errorf("read payload file: %w", err)
		}
		return b, nil
	default:
		return []byte(v), nil
	}
}
