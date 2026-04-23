// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"time"

	"github.com/kelseyhightower/envconfig"
)

type Mastermind struct {
	DatabaseURL       string        `envconfig:"DATABASE_URL" required:"true"`
	GRPCListenAddr    string        `envconfig:"GRPC_LISTEN_ADDR" default:":50051"`
	HTTPListenAddr    string        `envconfig:"HTTP_LISTEN_ADDR" default:":8080"`
	WorkerToken       string        `envconfig:"WORKER_TOKEN" required:"true"`
	AdminUser         string        `envconfig:"ADMIN_USER" required:"true"`
	AdminPassword     string        `envconfig:"ADMIN_PASSWORD" required:"true"`
	VisibilityTimeout time.Duration `envconfig:"VISIBILITY_TIMEOUT" default:"30s"`
	ReaperInterval    time.Duration `envconfig:"REAPER_INTERVAL" default:"10s"`
	LogLevel          string        `envconfig:"LOG_LEVEL" default:"info"`
	LogFormat         string        `envconfig:"LOG_FORMAT" default:"json"`
}

type Worker struct {
	MastermindAddr    string        `envconfig:"MASTERMIND_ADDR" required:"true"`
	WorkerToken       string        `envconfig:"WORKER_TOKEN" required:"true"`
	PollInterval      time.Duration `envconfig:"POLL_INTERVAL" default:"2s"`
	HeartbeatInterval time.Duration `envconfig:"HEARTBEAT_INTERVAL" default:"10s"`
	LogLevel          string        `envconfig:"LOG_LEVEL" default:"info"`
	LogFormat         string        `envconfig:"LOG_FORMAT" default:"json"`
}

func LoadMastermind() (Mastermind, error) {
	var c Mastermind
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("required key DATABASE_URL missing value")
	}
	if c.WorkerToken == "" {
		return c, fmt.Errorf("required key WORKER_TOKEN missing value")
	}
	if c.AdminUser == "" {
		return c, fmt.Errorf("required key ADMIN_USER missing value")
	}
	if c.AdminPassword == "" {
		return c, fmt.Errorf("required key ADMIN_PASSWORD missing value")
	}
	return c, nil
}

func LoadWorker() (Worker, error) {
	var c Worker
	if err := envconfig.Process("", &c); err != nil {
		return c, err
	}
	if c.MastermindAddr == "" {
		return c, fmt.Errorf("required key MASTERMIND_ADDR missing value")
	}
	if c.WorkerToken == "" {
		return c, fmt.Errorf("required key WORKER_TOKEN missing value")
	}
	return c, nil
}
