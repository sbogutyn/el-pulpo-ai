// Package cli implements the elpulpo command — a thin wrapper around
// mastermind's admin gRPC surface suitable for ad-hoc operator use from a
// shell. It shares its configuration conventions (MASTERMIND_ADDR,
// ADMIN_TOKEN, MASTERMIND_TLS) with the mastermind-mcp binary so the same
// environment serves both.
package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/kelseyhightower/envconfig"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/sbogutyn/el-pulpo-ai/internal/auth"
	pb "github.com/sbogutyn/el-pulpo-ai/internal/proto"
)

// Config is the runtime configuration for the elpulpo CLI. It intentionally
// mirrors mcpserver.Config so operators can re-use one set of env vars.
type Config struct {
	MastermindAddr string        `envconfig:"MASTERMIND_ADDR"`
	AdminToken     string        `envconfig:"ADMIN_TOKEN"`
	TLS            bool          `envconfig:"MASTERMIND_TLS" default:"false"`
	DialTimeout    time.Duration `envconfig:"DIAL_TIMEOUT" default:"5s"`
	RequestTimeout time.Duration `envconfig:"REQUEST_TIMEOUT" default:"15s"`
}

// LoadConfig reads config from the environment. It does not parse CLI flags
// for global settings — per-command flags can still override specific fields
// via the global flag set attached to each subcommand.
func LoadConfig() (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	return c, nil
}

// Run is the CLI entry point. It dispatches to a subcommand using args[0],
// wires stdout/stderr from the provided IO, and returns a non-nil error on
// any user-visible failure.
//
// Run takes args without the program name (the caller passes os.Args[1:]),
// mirroring how other CLIs in this repo consume arguments.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stderr)
		return errors.New("missing command")
	}
	// Global help short-circuits before we try to open a connection.
	switch args[0] {
	case "-h", "--help", "help":
		printUsage(stdout)
		return nil
	}

	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	group, rest := args[0], args[1:]
	switch group {
	case "tasks":
		return runTasks(ctx, cfg, rest, stdout, stderr)
	case "workers":
		return runWorkers(ctx, cfg, rest, stdout, stderr)
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command %q", group)
	}
}

// newClientConn opens a gRPC connection to mastermind. It is a package-level
// variable so tests can substitute an in-memory bufconn connection without
// binding to a real port or needing a running mastermind.
var newClientConn = func(_ context.Context, cfg Config) (grpc.ClientConnInterface, func() error, error) {
	if cfg.MastermindAddr == "" {
		return nil, nil, errors.New("MASTERMIND_ADDR is required")
	}
	if cfg.AdminToken == "" {
		return nil, nil, errors.New("ADMIN_TOKEN is required")
	}
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
		return nil, nil, fmt.Errorf("dial %s: %w", cfg.MastermindAddr, err)
	}
	return conn, conn.Close, nil
}

// closerFunc adapts a close function into io.Closer.
type closerFunc func() error

func (c closerFunc) Close() error { return c() }

func newAdminClient(ctx context.Context, cfg Config) (pb.AdminServiceClient, io.Closer, error) {
	conn, close, err := newClientConn(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	return pb.NewAdminServiceClient(conn), closerFunc(close), nil
}

// parseFlags is a small helper that sets the output writer on a flag set,
// parses the given args, and returns the flag set's remaining positional
// arguments. It keeps the per-command code free of boilerplate.
func parseFlags(fs *flag.FlagSet, stderr io.Writer, args []string) ([]string, error) {
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return fs.Args(), nil
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `elpulpo — admin CLI for the mastermind task queue

Usage:
  elpulpo tasks create        --name NAME [--instructions TEXT|@file|-] [--payload JSON]
                              [--priority N] [--max-attempts N] [--scheduled-for RFC3339]
                              [--jira-url URL] [--github-pr-url URL]
  elpulpo tasks get           <id>
  elpulpo tasks list          [--status STATUS] [--limit N] [--offset N] [--json]
  elpulpo tasks cancel        <id>
  elpulpo tasks retry         <id>
  elpulpo tasks request-review <id>
  elpulpo tasks finalize      <id> --success | --fail "reason"
  elpulpo workers list        [--json]

Environment:
  MASTERMIND_ADDR   host:port of mastermind gRPC (required)
  ADMIN_TOKEN       bearer token for AdminService (required)
  MASTERMIND_TLS    "true" to dial with TLS (default false)
  DIAL_TIMEOUT      connection deadline (default 5s)
  REQUEST_TIMEOUT   per-RPC deadline       (default 15s)
`)
}
