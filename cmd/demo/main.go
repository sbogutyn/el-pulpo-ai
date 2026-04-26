// Command demo is a development-time helper that simulates real workers and a
// task source against a running mastermind. It exists so the dashboard has
// something to display when there is no on-host coding agent driving real
// workers via MCP.
//
// Subcommands:
//
//	demo worker   — claim tasks, fake progress notes + log lines, complete (or rarely fail).
//	demo seeder   — periodically push new pending tasks via the AdminService.
//
// Both subcommands read configuration from the environment so they can be
// dropped into docker-compose alongside the real binaries.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
	"github.com/sbogutyn/el-pulpo-ai/internal/worker/taskclient"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var err error
	switch os.Args[1] {
	case "worker":
		err = runWorker(ctx, log)
	case "seeder":
		err = runSeeder(ctx, log)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Error("demo: fatal", "error", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `demo — synthetic workers and seeder for the el-pulpo-ai dashboard demo

usage:
  demo worker     run one fake worker (env: MASTERMIND_ADDR, WORKER_TOKEN, WORKER_ID)
  demo seeder     periodically create tasks (env: MASTERMIND_ADDR, ADMIN_TOKEN, SEEDER_INTERVAL)
`)
}

// ── shared helpers ─────────────────────────────────────────────────

func envRequired(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", fmt.Errorf("required env var %s is empty", key)
	}
	return v, nil
}

func envDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// dialBearer connects to the mastermind with a bearer-token interceptor for
// the given token. Plaintext gRPC — fine inside a docker network for the demo
// stack; do not point this at anything public.
func dialBearer(addr, token string) (*grpc.ClientConn, error) {
	return grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.BearerCredentials(token)),
	)
}

// ── worker subcommand ──────────────────────────────────────────────

func runWorker(ctx context.Context, log *slog.Logger) error {
	addr, err := envRequired("MASTERMIND_ADDR")
	if err != nil {
		return err
	}
	token, err := envRequired("WORKER_TOKEN")
	if err != nil {
		return err
	}
	id := os.Getenv("WORKER_ID")
	if id == "" {
		id = fmt.Sprintf("demo-%04d", rand.IntN(10000))
	}
	pollInterval := envDuration("POLL_INTERVAL", 1500*time.Millisecond)
	heartbeat := envDuration("HEARTBEAT_INTERVAL", 5*time.Second)
	minWork := envDuration("DEMO_MIN_WORK", 6*time.Second)
	maxWork := envDuration("DEMO_MAX_WORK", 18*time.Second)
	failPct := envInt("DEMO_FAIL_PCT", 5)

	log = log.With("component", "demo-worker", "worker_id", id)
	log.Info("starting", "mastermind", addr, "poll", pollInterval, "min_work", minWork, "max_work", maxWork)

	conn, err := dialBearer(addr, token)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	client := taskclient.NewClient(pb.NewTaskServiceClient(conn), id)

	for ctx.Err() == nil {
		task, err := client.Claim(ctx)
		if errors.Is(err, taskclient.ErrNoTask) {
			sleep(ctx, pollInterval)
			continue
		}
		if err != nil {
			log.Warn("claim", "error", err)
			sleep(ctx, pollInterval)
			continue
		}
		runOneTask(ctx, log, task, heartbeat, minWork, maxWork, failPct)
	}
	return ctx.Err()
}

func runOneTask(
	ctx context.Context, log *slog.Logger, task *taskclient.Task,
	heartbeat, minWork, maxWork time.Duration, failPct int,
) {
	log = log.With("task_id", task.ID(), "task_name", task.Name())
	log.Info("claimed")

	stopHB := task.StartHeartbeat(ctx, heartbeat, func(err error) {
		log.Warn("heartbeat", "error", err)
	})
	defer stopHB()

	totalWork := jitter(minWork, maxWork)
	steps := 3 + rand.IntN(4) // 3..6 progress beats
	stepGap := max(totalWork/time.Duration(steps), 500*time.Millisecond)

	for i := 1; i <= steps; i++ {
		if ctx.Err() != nil {
			return
		}
		note := fmt.Sprintf("step %d/%d · %s", i, steps, randomPhase())
		if err := task.Progress(ctx, note); err != nil {
			log.Warn("progress", "error", err)
		}
		if _, err := task.AppendLog(ctx, fmt.Sprintf("%s — %s", note, randomDetail())); err != nil {
			log.Warn("append_log", "error", err)
		}
		sleep(ctx, stepGap)
	}

	if rand.IntN(100) < failPct {
		msg := randomFailure()
		if _, err := task.AppendLog(ctx, "failure: "+msg); err != nil {
			log.Warn("append_log", "error", err)
		}
		if err := task.Fail(ctx, msg); err != nil {
			log.Warn("fail", "error", err)
		}
		log.Info("failed", "msg", msg)
		return
	}

	if _, err := task.AppendLog(ctx, "done"); err != nil {
		log.Warn("append_log", "error", err)
	}
	if err := task.Complete(ctx); err != nil {
		log.Warn("complete", "error", err)
		return
	}
	log.Info("completed")
}

func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func jitter(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	return lo + time.Duration(rand.Int64N(int64(hi-lo)))
}

var phaseWords = []string{
	"loading inputs", "tokenizing batch", "calling model", "scoring outputs",
	"writing artifacts", "verifying", "uploading results", "compacting",
	"reranking", "summarizing", "embedding", "indexing", "warming cache",
}

var detailWords = []string{
	"412 rows", "8.4kb chunk", "p50=120ms", "cache hit",
	"context 14201/120k tokens", "batch 12/14", "rate limit ok",
	"connection reused", "schema validated", "checksum ok",
	"retry budget intact", "leasing extension confirmed",
}

var failures = []string{
	"upstream timeout after 5s",
	"parse error on row 88",
	"rate-limited by provider, gave up",
	"unexpected schema in payload",
	"out of memory while embedding",
}

func randomPhase() string  { return phaseWords[rand.IntN(len(phaseWords))] }
func randomDetail() string { return detailWords[rand.IntN(len(detailWords))] }
func randomFailure() string {
	return failures[rand.IntN(len(failures))]
}

// ── seeder subcommand ──────────────────────────────────────────────

func runSeeder(ctx context.Context, log *slog.Logger) error {
	addr, err := envRequired("MASTERMIND_ADDR")
	if err != nil {
		return err
	}
	token, err := envRequired("ADMIN_TOKEN")
	if err != nil {
		return err
	}
	interval := envDuration("SEEDER_INTERVAL", 8*time.Second)
	burst := envInt("SEEDER_BURST", 2)
	target := envInt("SEEDER_TARGET_BACKLOG", 12)

	log = log.With("component", "demo-seeder")
	log.Info("starting", "mastermind", addr, "interval", interval, "burst", burst, "target_backlog", target)

	conn, err := dialBearer(addr, token)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	admin := pb.NewAdminServiceClient(conn)

	// Seed some tasks immediately so the dashboard isn't empty on first paint.
	if err := seedBatch(ctx, admin, burst*2); err != nil {
		log.Warn("initial seed", "error", err)
	}

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			pending, err := countPending(ctx, admin)
			if err != nil {
				log.Warn("list pending", "error", err)
				continue
			}
			needed := min(max(target-pending, burst), 6)
			if err := seedBatch(ctx, admin, needed); err != nil {
				log.Warn("seed batch", "error", err)
			} else {
				log.Info("seeded", "count", needed, "queue_was", pending)
			}
		}
	}
}

func countPending(ctx context.Context, admin pb.AdminServiceClient) (int, error) {
	resp, err := admin.ListTasks(ctx, &pb.ListTasksRequest{Status: "pending", Limit: 200})
	if err != nil {
		return 0, err
	}
	return len(resp.GetItems()), nil
}

func seedBatch(ctx context.Context, admin pb.AdminServiceClient, n int) error {
	for range n {
		name, prio := randomTask()
		payload, _ := json.Marshal(map[string]any{
			"kind":   name,
			"size":   rand.IntN(500) + 1,
			"seed":   rand.Int64(),
			"source": "demo-seeder",
		})
		_, err := admin.CreateTask(ctx, &pb.CreateTaskRequest{
			Name:        name,
			Payload:     payload,
			Priority:    int32(prio),
			MaxAttempts: 3,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

var taskNames = []string{
	"classify-support-tickets",
	"summarize-call-transcripts",
	"extract-invoice-line-items",
	"embed-product-catalog-delta",
	"translate-fr-en-doc-set",
	"image-moderation-sweep",
	"generate-weekly-report-draft",
	"sentiment-scoring-run",
	"ocr-receipts-batch",
	"rerank-search-results",
	"deduplicate-customer-records",
	"render-pdf-statements",
}

func randomTask() (string, int) {
	name := taskNames[rand.IntN(len(taskNames))]
	// Skewed priority: ~50% low (0-2), 35% mid (3-6), 15% high (7-10).
	r := rand.IntN(100)
	switch {
	case r < 50:
		return name, rand.IntN(3)
	case r < 85:
		return name, 3 + rand.IntN(4)
	default:
		return name, 7 + rand.IntN(4)
	}
}
