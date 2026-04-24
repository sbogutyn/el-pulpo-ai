// Command mastermind-mcp is a stdio MCP server that exposes mastermind's
// AdminService as MCP tools. Spawned as a subprocess by a coding agent.
//
// stdout is the MCP framing channel; all logs MUST go to stderr.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	"github.com/sbogutyn/el-pulpo-ai/internal/mcpserver"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mastermind-mcp:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := mcpserver.Load(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	log := newLogger(cfg.LogLevel, cfg.LogFormat)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	dialCtx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()

	var tc credentials.TransportCredentials
	if cfg.TLS {
		tc = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	} else {
		tc = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(cfg.MastermindAddr,
		grpc.WithTransportCredentials(tc),
		grpc.WithPerRPCCredentials(auth.BearerCredentials(cfg.AdminToken)))
	if err != nil {
		return fmt.Errorf("dial %s: %w", cfg.MastermindAddr, err)
	}
	defer conn.Close()

	// Startup probe: one tiny ListTasks call within dialCtx. Fails fast on
	// unreachable mastermind or a rejected admin token, before the coding
	// agent ever sees the MCP handshake succeed.
	client := pb.NewAdminServiceClient(conn)
	if _, err := client.ListTasks(dialCtx, &pb.ListTasksRequest{Limit: 1}); err != nil {
		return fmt.Errorf("probe mastermind: %w", err)
	}

	srv := mcpserver.NewServer(client)
	log.Info("mastermind-mcp: ready",
		"mastermind_addr", cfg.MastermindAddr, "tls", cfg.TLS)

	// Run the MCP stdio loop until stdin EOF, signal, or transport error.
	return srv.Run(ctx, &mcp.StdioTransport{})
}

func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	_ = lvl.UnmarshalText([]byte(level))
	opts := &slog.HandlerOptions{Level: lvl}
	if format == "text" {
		return slog.New(slog.NewTextHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, opts))
}
