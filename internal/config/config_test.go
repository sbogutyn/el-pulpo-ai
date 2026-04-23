package config

import (
	"testing"
	"time"
)

func setEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func TestLoadMastermind_Defaults(t *testing.T) {
	setEnv(t, map[string]string{
		"DATABASE_URL":   "postgres://u:p@localhost/db",
		"WORKER_TOKEN":   "tok",
		"ADMIN_USER":     "admin",
		"ADMIN_PASSWORD": "pw",
	})

	cfg, err := LoadMastermind()
	if err != nil {
		t.Fatalf("LoadMastermind: %v", err)
	}
	if cfg.GRPCListenAddr != ":50051" {
		t.Errorf("GRPCListenAddr: got %q", cfg.GRPCListenAddr)
	}
	if cfg.HTTPListenAddr != ":8080" {
		t.Errorf("HTTPListenAddr: got %q", cfg.HTTPListenAddr)
	}
	if cfg.VisibilityTimeout != 30*time.Second {
		t.Errorf("VisibilityTimeout: got %v", cfg.VisibilityTimeout)
	}
	if cfg.ReaperInterval != 10*time.Second {
		t.Errorf("ReaperInterval: got %v", cfg.ReaperInterval)
	}
	if cfg.LogLevel != "info" || cfg.LogFormat != "json" {
		t.Errorf("log defaults wrong: level=%q format=%q", cfg.LogLevel, cfg.LogFormat)
	}
}

func TestLoadMastermind_MissingRequired(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("WORKER_TOKEN", "")
	t.Setenv("ADMIN_USER", "")
	t.Setenv("ADMIN_PASSWORD", "")
	if _, err := LoadMastermind(); err == nil {
		t.Fatal("expected error for missing required vars")
	}
}

func TestLoadWorker_Defaults(t *testing.T) {
	setEnv(t, map[string]string{
		"MASTERMIND_ADDR": "mastermind:50051",
		"WORKER_TOKEN":    "tok",
	})

	cfg, err := LoadWorker()
	if err != nil {
		t.Fatalf("LoadWorker: %v", err)
	}
	if cfg.PollInterval != 2*time.Second {
		t.Errorf("PollInterval: got %v", cfg.PollInterval)
	}
	if cfg.HeartbeatInterval != 10*time.Second {
		t.Errorf("HeartbeatInterval: got %v", cfg.HeartbeatInterval)
	}
}
