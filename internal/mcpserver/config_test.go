package mcpserver

import (
	"testing"
	"time"
)

func TestLoad_EnvOnly(t *testing.T) {
	t.Setenv("MASTERMIND_ADDR", "localhost:50051")
	t.Setenv("ADMIN_TOKEN", "tok")

	c, err := Load(nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.MastermindAddr != "localhost:50051" {
		t.Errorf("Addr=%q", c.MastermindAddr)
	}
	if c.AdminToken != "tok" {
		t.Errorf("Token=%q", c.AdminToken)
	}
	if c.DialTimeout != 5*time.Second {
		t.Errorf("DialTimeout=%v, want 5s", c.DialTimeout)
	}
}

func TestLoad_FlagOverridesEnv(t *testing.T) {
	t.Setenv("MASTERMIND_ADDR", "env-addr:1")
	t.Setenv("ADMIN_TOKEN", "env-tok")

	c, err := Load([]string{"--addr", "flag-addr:2", "--token", "flag-tok", "--tls", "--dial-timeout", "1s"})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.MastermindAddr != "flag-addr:2" {
		t.Errorf("Addr=%q, want flag-addr:2", c.MastermindAddr)
	}
	if c.AdminToken != "flag-tok" {
		t.Errorf("Token=%q, want flag-tok", c.AdminToken)
	}
	if !c.TLS {
		t.Error("TLS should be true")
	}
	if c.DialTimeout != time.Second {
		t.Errorf("DialTimeout=%v, want 1s", c.DialTimeout)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	t.Setenv("MASTERMIND_ADDR", "")
	t.Setenv("ADMIN_TOKEN", "")

	_, err := Load(nil)
	if err == nil {
		t.Fatal("want error for missing MASTERMIND_ADDR / ADMIN_TOKEN")
	}
}
