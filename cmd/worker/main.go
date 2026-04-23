// Command worker connects to the mastermind over gRPC and processes tasks.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	"github.com/sbogutyn/el-pulpo-ai/internal/config"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
	"github.com/sbogutyn/el-pulpo-ai/internal/worker/runner"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker: fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}
	log := newLogger(cfg.LogLevel, cfg.LogFormat).With("component", "worker")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	conn, err := grpc.NewClient(cfg.MastermindAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(auth.BearerCredentials(cfg.WorkerToken)),
	)
	if err != nil {
		return err
	}
	defer conn.Close()

	id := uuid.New().String()
	log = log.With("worker_id", id)
	log.Info("starting", "mastermind_addr", cfg.MastermindAddr)

	r := runner.New(pb.NewTaskServiceClient(conn), runner.Config{
		WorkerID:          id,
		PollInterval:      cfg.PollInterval,
		HeartbeatInterval: cfg.HeartbeatInterval,
		WorkDuration:      time.Minute,
	}, log)

	r.Run(ctx)
	log.Info("stopped")
	return nil
}

func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(level))
	opts := &slog.HandlerOptions{Level: lvl}
	if format == "text" {
		return slog.New(slog.NewTextHandler(os.Stdout, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, opts))
}
