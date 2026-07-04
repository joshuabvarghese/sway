// Package agent contains the monitoring agent and its metric types.
package agent

import "time"

// NodeMetrics holds the computed, normalised metrics for one cluster node.
type NodeMetrics struct {
	NodeID     string
	NodeName   string
	Host       string
	Roles      []string
	IsDataNode bool

	// Raw resource readings.
	JVMHeapPercent  float64 // 0–100
	DiskUsedPercent float64 // 0–100
	DiskTotalBytes  int64
	DiskUsedBytes   int64
	ShardCount      int
	TotalShardBytes int64

	// Derived latency (average from node stats; see CircuitBreaker note in config).
	AvgSearchLatMs float64

	// Weighted hot-score: JVMWeight*heap + DiskWeight*disk + ShardWeight*shardFraction.
	HotScore float64
	// IsHot is true when HotScore >= AgentConfig.HotNodeThreshold.
	IsHot bool
}

// ShardInfo describes a single shard placement in the cluster.
type ShardInfo struct {
	Index     string
	ShardNum  int
	Primary   bool   // true = primary, false = replica
	NodeID    string // node currently hosting this shard
	State     string // STARTED, RELOCATING, INITIALIZING, UNASSIGNED
	SizeBytes int64  // from _cat/shards; 0 if unavailable
}

// ShardKey returns a stable string key for this shard's identity (not placement).
// Used to detect when a replica already lives on a proposed destination node.
func (s *ShardInfo) ShardKey() string {
	role := "r"
	if s.Primary {
		role = "p"
	}
	return s.Index + "/" + itoa(s.ShardNum) + "/" + role
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

// ClusterSnapshot is a point-in-time view of the whole cluster,
// captured by the MonitoringAgent.
type ClusterSnapshot struct {
	Timestamp        time.Time
	ClusterName      string
	Health           string
	NodeMetrics      map[string]*NodeMetrics // nodeID → metrics
	Shards           []ShardInfo
	TotalShards      int
	RelocatingShards int
	SkewScore        float64 // std-dev of HotScore across data nodes
	AvgSearchLatMs   float64 // cluster-wide average
}

// DataNodes returns only the subset of NodeMetrics for data nodes.
func (s *ClusterSnapshot) DataNodes() []*NodeMetrics {
	out := make([]*NodeMetrics, 0, len(s.NodeMetrics))
	for _, n := range s.NodeMetrics {
		if n.IsDataNode {
			out = append(out, n)
		}
	}
	return out
}

// ShardsOnNode returns all shards currently placed on nodeID.
func (s *ClusterSnapshot) ShardsOnNode(nodeID string) []ShardInfo {
	var out []ShardInfo
	for _, sh := range s.Shards {
		if sh.NodeID == nodeID {
			out = append(out, sh)
		}
	}
	return out
}
