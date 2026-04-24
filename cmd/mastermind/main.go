// Command mastermind runs the gRPC TaskService, the HTMX admin UI, the
// reaper, and Prometheus metrics against a Postgres instance.
package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"google.golang.org/grpc"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	"github.com/sbogutyn/el-pulpo-ai/internal/config"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/grpcserver"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/httpserver"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/metrics"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/reaper"
	"github.com/sbogutyn/el-pulpo-ai/internal/mastermind/store"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

func main() {
	if err := run(); err != nil {
		slog.Error("mastermind: fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadMastermind()
	if err != nil {
		return err
	}

	log := newLogger(cfg.LogLevel, cfg.LogFormat).With("component", "mastermind")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := applyMigrations(cfg.DatabaseURL); err != nil {
		return err
	}
	log.Info("migrations: applied")

	s, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer s.Close()

	grpcLis, err := net.Listen("tcp", cfg.GRPCListenAddr)
	if err != nil {
		return err
	}
	policy := map[string]string{
		"/elpulpo.tasks.v1.TaskService/ClaimTask":      cfg.WorkerToken,
		"/elpulpo.tasks.v1.TaskService/Heartbeat":      cfg.WorkerToken,
		"/elpulpo.tasks.v1.TaskService/ReportResult":   cfg.WorkerToken,
		"/elpulpo.tasks.v1.TaskService/UpdateProgress": cfg.WorkerToken,
		"/elpulpo.tasks.v1.TaskService/AppendLog":      cfg.WorkerToken,
		"/elpulpo.tasks.v1.AdminService/CreateTask":    cfg.AdminToken,
		"/elpulpo.tasks.v1.AdminService/GetTask":       cfg.AdminToken,
		"/elpulpo.tasks.v1.AdminService/ListTasks":     cfg.AdminToken,
		"/elpulpo.tasks.v1.AdminService/ListTaskLogs":  cfg.AdminToken,
		"/elpulpo.tasks.v1.AdminService/CancelTask":    cfg.AdminToken,
		"/elpulpo.tasks.v1.AdminService/RetryTask":     cfg.AdminToken,
		"/elpulpo.tasks.v1.AdminService/ListWorkers":   cfg.AdminToken,
	}
	gs := grpc.NewServer(grpc.UnaryInterceptor(auth.PerMethodInterceptor(policy)))
	pb.RegisterTaskServiceServer(gs, grpcserver.New(s))
	pb.RegisterAdminServiceServer(gs, grpcserver.NewAdmin(s))
	grpcErrCh := make(chan error, 1)
	go func() {
		log.Info("grpc: listening", "addr", cfg.GRPCListenAddr)
		grpcErrCh <- gs.Serve(grpcLis)
	}()

	hs, err := httpserver.New(s, httpserver.Config{AdminUser: cfg.AdminUser, AdminPassword: cfg.AdminPassword}, log)
	if err != nil {
		return err
	}
	httpErrCh := make(chan error, 1)
	go func() { httpErrCh <- hs.ListenAndServe(ctx, cfg.HTTPListenAddr) }()

	rp := reaper.New(s, cfg.ReaperInterval, cfg.VisibilityTimeout, log)
	go rp.Run(ctx)

	go samplePending(ctx, s, cfg.ReaperInterval, log)

	select {
	case err := <-grpcErrCh:
		return err
	case err := <-httpErrCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		stopped := make(chan struct{})
		go func() { gs.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(15 * time.Second):
			gs.Stop()
		}
		<-httpErrCh
	}
	return nil
}

func applyMigrations(dsn string) error {
	abs, err := filepath.Abs("migrations")
	if err != nil {
		return err
	}
	m, err := migrate.New("file://"+abs, dsn)
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return nil
}

func samplePending(ctx context.Context, s *store.Store, d time.Duration, log *slog.Logger) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n, err := s.CountPending(ctx)
			if err != nil {
				log.Warn("pending sample", "error", err)
				continue
			}
			metrics.TasksPending.Set(float64(n))
		}
	}
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
