package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault_IsSafeByDefault(t *testing.T) {
	cfg := Default()

	if !cfg.Rebalancer.DryRun {
		t.Error("Default() must have DryRun=true — safety-first default")
	}
	if cfg.CircuitBreaker.RequiredHealth != "green" {
		t.Errorf("expected RequiredHealth=green, got %q", cfg.CircuitBreaker.RequiredHealth)
	}
	if cfg.CircuitBreaker.MaxRelocatingShards != 0 {
		t.Errorf("expected MaxRelocatingShards=0, got %d", cfg.CircuitBreaker.MaxRelocatingShards)
	}

	sum := cfg.Agent.JVMWeight + cfg.Agent.DiskWeight + cfg.Agent.ShardWeight
	if sum < 0.999 || sum > 1.001 {
		t.Errorf("agent weights should sum to ~1.0, got %.4f", sum)
	}
}

func TestLoadFromFile_MissingFile_ReturnsError(t *testing.T) {
	_, err := LoadFromFile(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("expected an error for a missing config file, got nil")
	}
}

func TestLoadFromFile_OverlaysOnTopOfDefaults(t *testing.T) {
	// Only override one field; everything else should keep its Default() value.
	path := filepath.Join(t.TempDir(), "config.json")
	partial := `{"rebalancer": {"dry_run": false, "max_moves_per_cycle": 5, "skew_reduction_target": 0.25, "large_shard_threshold_bytes": 2147483648}}`
	if err := os.WriteFile(path, []byte(partial), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	cfg, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile returned error: %v", err)
	}

	if cfg.Rebalancer.DryRun {
		t.Error("expected dry_run override (false) to take effect")
	}
	// Untouched sections should still carry defaults.
	if cfg.CircuitBreaker.RequiredHealth != "green" {
		t.Errorf("expected untouched circuit_breaker section to keep default, got %q", cfg.CircuitBreaker.RequiredHealth)
	}
	if cfg.Agent.HotNodeThreshold != 0.70 {
		t.Errorf("expected untouched agent section to keep default threshold, got %.2f", cfg.Agent.HotNodeThreshold)
	}
}

func TestPollInterval_ConvertsSecondsToDuration(t *testing.T) {
	a := AgentConfig{PollIntervalSeconds: 30}
	if got := a.PollInterval(); got.Seconds() != 30 {
		t.Errorf("expected 30s, got %v", got)
	}
}
