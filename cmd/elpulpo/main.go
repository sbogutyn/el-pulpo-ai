// Command elpulpo is a small admin CLI that wraps the mastermind AdminService
// gRPC surface. It is faster than curling the HTMX admin UI for ad-hoc ops
// and reuses the same bearer-token configuration as mastermind-mcp, so an
// operator can point both at the same environment with no extra wiring.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sbogutyn/el-pulpo-ai/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "elpulpo:", err)
		os.Exit(1)
	}
}
