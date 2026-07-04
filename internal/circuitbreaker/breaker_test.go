package circuitbreaker

import (
	"io"
	"log"
	"testing"

	"github.com/project-sway/sway/internal/agent"
	"github.com/project-sway/sway/internal/config"
)

func testLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

func defaultCfg() config.CircuitBreakerConfig {
	return config.CircuitBreakerConfig{
		RequiredHealth:      "green",
		MaxAvgLatencyMs:     200.0,
		MaxRelocatingShards: 0,
	}
}

func snapshot(health string, latencyMs float64, relocating int) *agent.ClusterSnapshot {
	return &agent.ClusterSnapshot{
		Health:           health,
		AvgSearchLatMs:   latencyMs,
		RelocatingShards: relocating,
	}
}

func TestHealthSatisfies(t *testing.T) {
	cases := []struct {
		actual, required string
		want             bool
	}{
		{"green", "green", true},
		{"yellow", "green", false},
		{"green", "yellow", true},
		{"red", "green", false},
		{"red", "red", true},
		{"purple", "green", false}, // unknown status never satisfies
	}
	for _, c := range cases {
		got := healthSatisfies(c.actual, c.required)
		if got != c.want {
			t.Errorf("healthSatisfies(%q, %q) = %v, want %v", c.actual, c.required, got, c.want)
		}
	}
}

func TestEvaluate_AllChecksPass_CircuitCloses(t *testing.T) {
	cb := New(defaultCfg(), testLogger())
	result := cb.Evaluate(snapshot("green", 50.0, 0))

	if result.State != StateClosed {
		t.Fatalf("expected StateClosed, got %v (reason: %s)", result.State, result.Reason)
	}
	if len(result.Checks) != 3 {
		t.Fatalf("expected 3 checks, got %d", len(result.Checks))
	}
	for _, c := range result.Checks {
		if !c.Passed {
			t.Errorf("expected check %q to pass, it failed", c.Name)
		}
	}
}

func TestEvaluate_UnhealthyCluster_CircuitOpens(t *testing.T) {
	cb := New(defaultCfg(), testLogger())
	result := cb.Evaluate(snapshot("yellow", 50.0, 0))

	if result.State != StateOpen {
		t.Fatalf("expected StateOpen when health is yellow, got %v", result.State)
	}
	if result.Reason == "" {
		t.Error("expected a non-empty reason when circuit opens")
	}
}

func TestEvaluate_HighLatency_CircuitOpens(t *testing.T) {
	cb := New(defaultCfg(), testLogger())
	result := cb.Evaluate(snapshot("green", 250.0, 0))

	if result.State != StateOpen {
		t.Fatalf("expected StateOpen when latency exceeds threshold, got %v", result.State)
	}
}

func TestEvaluate_ActiveRelocations_CircuitOpens(t *testing.T) {
	cb := New(defaultCfg(), testLogger())
	result := cb.Evaluate(snapshot("green", 50.0, 3))

	if result.State != StateOpen {
		t.Fatalf("expected StateOpen when shards are relocating, got %v", result.State)
	}
}

func TestEvaluate_MultipleFailures_AllNamedInReason(t *testing.T) {
	cb := New(defaultCfg(), testLogger())
	result := cb.Evaluate(snapshot("red", 500.0, 5))

	if result.State != StateOpen {
		t.Fatalf("expected StateOpen, got %v", result.State)
	}
	for _, name := range []string{"Cluster Health", "Avg Search Latency", "Relocating Shards"} {
		found := false
		for _, c := range result.Checks {
			if c.Name == name && !c.Passed {
				found = true
			}
		}
		if !found {
			t.Errorf("expected failed check %q to be reported", name)
		}
	}
}

func TestLast_ReturnsMostRecentEvaluation(t *testing.T) {
	cb := New(defaultCfg(), testLogger())
	if cb.Last() != nil {
		t.Fatal("expected Last() to be nil before any Evaluate call")
	}
	cb.Evaluate(snapshot("green", 10, 0))
	last := cb.Last()
	if last == nil || last.State != StateClosed {
		t.Fatal("expected Last() to return the most recent closed result")
	}
}

func TestState_String(t *testing.T) {
	if StateClosed.String() != "CLOSED" {
		t.Errorf("expected CLOSED, got %s", StateClosed.String())
	}
	if StateOpen.String() != "OPEN" {
		t.Errorf("expected OPEN, got %s", StateOpen.String())
	}
}
