// Package mcpserver wires the mastermind admin gRPC client to the official
// MCP Go SDK, registering tools that let a coding agent create and inspect
// tasks on mastermind.
package mcpserver

import (
	"flag"
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

// Config is the runtime configuration for the mastermind-mcp binary.
type Config struct {
	MastermindAddr string        `envconfig:"MASTERMIND_ADDR"`
	AdminToken     string        `envconfig:"ADMIN_TOKEN"`
	TLS            bool          `envconfig:"MASTERMIND_TLS" default:"false"`
	DialTimeout    time.Duration `envconfig:"DIAL_TIMEOUT" default:"5s"`
	LogLevel       string        `envconfig:"LOG_LEVEL" default:"info"`
	LogFormat      string        `envconfig:"LOG_FORMAT" default:"json"`
}

// Load reads config from the environment, then applies CLI flag overrides.
// Passing nil args loads env only (useful for tests and library consumers).
func Load(args []string) (Config, error) {
	var c Config
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	fs := flag.NewFlagSet("mastermind-mcp", flag.ContinueOnError)
	fs.StringVar(&c.MastermindAddr, "addr", c.MastermindAddr, "mastermind gRPC address (env: MASTERMIND_ADDR)")
	fs.StringVar(&c.AdminToken, "token", c.AdminToken, "admin bearer token (env: ADMIN_TOKEN)")
	fs.BoolVar(&c.TLS, "tls", c.TLS, "dial mastermind with TLS (env: MASTERMIND_TLS)")
	fs.DurationVar(&c.DialTimeout, "dial-timeout", c.DialTimeout, "startup dial deadline (env: DIAL_TIMEOUT)")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "log level (env: LOG_LEVEL)")
	fs.StringVar(&c.LogFormat, "log-format", c.LogFormat, "log format json|text (env: LOG_FORMAT)")
	if args != nil {
		if err := fs.Parse(args); err != nil {
			return c, err
		}
	}
	if c.MastermindAddr == "" {
		return c, fmt.Errorf("MASTERMIND_ADDR (or --addr) is required")
	}
	if c.AdminToken == "" {
		return c, fmt.Errorf("ADMIN_TOKEN (or --token) is required")
	}
	return c, nil
}
