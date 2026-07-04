package rebalancer

import (
	"testing"

	"github.com/project-sway/sway/internal/agent"
	"github.com/project-sway/sway/internal/config"
)

func testAgentCfg() config.AgentConfig {
	return config.AgentConfig{
		HotNodeThreshold: 0.50,
		JVMWeight:        0.40,
		DiskWeight:       0.40,
		ShardWeight:      0.20,
	}
}

// buildSnapshot constructs a minimal two-hot / one-cool cluster:
//   - node-hot:  90% JVM, 80% disk, 4 shards — clearly hot
//   - node-cool: 10% JVM, 10% disk, 0 shards — clearly cool
//   - node-full: 10% JVM, 10% disk, but disk_total is tiny, so it's over capacity
//     the instant anything lands on it.
func buildSnapshot() *agent.ClusterSnapshot {
	nodeMetrics := map[string]*agent.NodeMetrics{
		"n-hot": {
			NodeID: "n-hot", NodeName: "node-hot", IsDataNode: true,
			JVMHeapPercent: 90, DiskUsedPercent: 80,
			DiskTotalBytes: 1000, ShardCount: 4, TotalShardBytes: 800,
		},
		"n-cool": {
			NodeID: "n-cool", NodeName: "node-cool", IsDataNode: true,
			JVMHeapPercent: 10, DiskUsedPercent: 10,
			DiskTotalBytes: 1000, ShardCount: 0, TotalShardBytes: 0,
		},
		"n-full": {
			NodeID: "n-full", NodeName: "node-full", IsDataNode: true,
			JVMHeapPercent: 10, DiskUsedPercent: 10,
			DiskTotalBytes: 100, ShardCount: 0, TotalShardBytes: 85,
		},
	}

	shards := []agent.ShardInfo{
		{Index: "logs", ShardNum: 0, Primary: true, NodeID: "n-hot", State: "STARTED", SizeBytes: 500},
		{Index: "logs", ShardNum: 0, Primary: false, NodeID: "n-cool", State: "STARTED", SizeBytes: 500},
		{Index: "logs", ShardNum: 1, Primary: true, NodeID: "n-hot", State: "STARTED", SizeBytes: 300},
	}

	return &agent.ClusterSnapshot{
		Health:      "green",
		NodeMetrics: nodeMetrics,
		Shards:      shards,
	}
}

func TestGenerate_MovesShardsOffHotNodes(t *testing.T) {
	g := NewTargetStateGenerator(testAgentCfg(), config.RebalancerConfig{
		MaxMovesPerCycle:    5,
		SkewReductionTarget: 0.99, // force it to keep moving until candidates run out
	})

	ts, err := g.Generate(buildSnapshot())
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(ts.Moves) == 0 {
		t.Fatal("expected at least one planned move off the hot node")
	}
	for _, m := range ts.Moves {
		if m.FromNode != "n-hot" {
			t.Errorf("expected moves to originate from n-hot, got %s", m.FromNode)
		}
	}
}

func TestGenerate_LargestShardMovedFirst(t *testing.T) {
	// Two shards on the hot node, neither blocked by a co-location or capacity
	// guard, so size is the only thing that determines move order.
	snap := &agent.ClusterSnapshot{
		Health: "green",
		NodeMetrics: map[string]*agent.NodeMetrics{
			"n-hot": {
				NodeID: "n-hot", NodeName: "node-hot", IsDataNode: true,
				JVMHeapPercent: 90, DiskUsedPercent: 80,
				DiskTotalBytes: 1000, ShardCount: 2, TotalShardBytes: 800,
			},
			"n-cool": {
				NodeID: "n-cool", NodeName: "node-cool", IsDataNode: true,
				JVMHeapPercent: 10, DiskUsedPercent: 10,
				DiskTotalBytes: 1000, ShardCount: 0, TotalShardBytes: 0,
			},
		},
		Shards: []agent.ShardInfo{
			{Index: "logs", ShardNum: 0, Primary: true, NodeID: "n-hot", State: "STARTED", SizeBytes: 500},
			{Index: "traces", ShardNum: 0, Primary: true, NodeID: "n-hot", State: "STARTED", SizeBytes: 300},
		},
	}

	g := NewTargetStateGenerator(testAgentCfg(), config.RebalancerConfig{
		MaxMovesPerCycle:    1, // only one move — must be the larger shard
		SkewReductionTarget: 0.99,
	})

	ts, err := g.Generate(snap)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(ts.Moves) != 1 {
		t.Fatalf("expected exactly 1 move, got %d", len(ts.Moves))
	}
	if ts.Moves[0].Shard.SizeBytes != 500 {
		t.Errorf("expected the larger 500-byte shard to move first, got %d bytes", ts.Moves[0].Shard.SizeBytes)
	}
}

func TestGenerate_NeverColocatesPrimaryWithItsReplica(t *testing.T) {
	g := NewTargetStateGenerator(testAgentCfg(), config.RebalancerConfig{
		MaxMovesPerCycle:    5,
		SkewReductionTarget: 0.99,
	})

	ts, err := g.Generate(buildSnapshot())
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	for _, m := range ts.Moves {
		// logs[0]'s replica already lives on n-cool — the primary must never
		// be planned to move there too.
		if m.Shard.Index == "logs" && m.Shard.ShardNum == 0 && m.ToNode == "n-cool" {
			t.Error("planned move would co-locate a primary with its own replica")
		}
	}
}

func TestGenerate_SkipsNodesThatWouldExceedCapacity(t *testing.T) {
	g := NewTargetStateGenerator(testAgentCfg(), config.RebalancerConfig{
		MaxMovesPerCycle:    5,
		SkewReductionTarget: 0.99,
	})

	ts, err := g.Generate(buildSnapshot())
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	for _, m := range ts.Moves {
		if m.ToNode == "n-full" {
			t.Error("planned move would push n-full over the 90%% capacity guard")
		}
	}
}

func TestGenerate_StopsAtMaxMovesPerCycle(t *testing.T) {
	g := NewTargetStateGenerator(testAgentCfg(), config.RebalancerConfig{
		MaxMovesPerCycle:    1,
		SkewReductionTarget: 0.99,
	})

	ts, err := g.Generate(buildSnapshot())
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(ts.Moves) > 1 {
		t.Errorf("expected at most 1 move (MaxMovesPerCycle=1), got %d", len(ts.Moves))
	}
}

func TestGenerate_StopsOnceReductionTargetMet(t *testing.T) {
	// A tiny target should be satisfied by the very first move.
	g := NewTargetStateGenerator(testAgentCfg(), config.RebalancerConfig{
		MaxMovesPerCycle:    5,
		SkewReductionTarget: 0.01,
	})

	ts, err := g.Generate(buildSnapshot())
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if !ts.TargetMet {
		t.Error("expected TargetMet to be true once skew reduction exceeds the tiny target")
	}
	if len(ts.Moves) == 0 {
		t.Fatal("expected at least one move to have been planned")
	}
}

func TestGenerate_SkipsUnplaceableShardAndUsesNextCandidate(t *testing.T) {
	// logs[0] (500 bytes) has nowhere legal to go: its only cool neighbour
	// already holds the replica, and the other node is over capacity.
	// The generator should skip it and place traces[0] (300 bytes) instead,
	// rather than giving up on the whole cycle.
	ts, err := NewTargetStateGenerator(testAgentCfg(), config.RebalancerConfig{
		MaxMovesPerCycle:    5,
		SkewReductionTarget: 0.99,
	}).Generate(buildSnapshot())
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if len(ts.Moves) == 0 {
		t.Fatal("expected the generator to still place the placeable shard")
	}
	for _, m := range ts.Moves {
		if m.Shard.Index == "logs" && m.Shard.ShardNum == 0 {
			t.Error("logs[0]/primary has no legal destination and should have been skipped, not moved")
		}
	}
}

func TestGenerate_NoDataNodes_ReturnsError(t *testing.T) {
	g := NewTargetStateGenerator(testAgentCfg(), config.RebalancerConfig{MaxMovesPerCycle: 5, SkewReductionTarget: 0.25})
	empty := &agent.ClusterSnapshot{NodeMetrics: map[string]*agent.NodeMetrics{}}

	if _, err := g.Generate(empty); err == nil {
		t.Fatal("expected an error when the snapshot has no data nodes")
	}
}
