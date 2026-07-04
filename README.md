# Sway

**A shard rebalancer for OpenSearch whose main feature is knowing when *not* to act.**

Sway watches an OpenSearch cluster for hot spots, computes the minimum set of shard moves needed to relieve them, and gates every single move behind a circuit breaker and a dry-run default. It ships with a deterministic 10-node cluster simulator, so you can watch the full monitor → gate → plan → execute loop converge from a skewed state to a balanced one with a single command and no OpenSearch cluster at all.

**[Live demo →](https://joshuabvarghese.github.io/Sway/)** &nbsp;·&nbsp; 20/20 tests passing &nbsp;·&nbsp; `go vet` clean &nbsp;·&nbsp; `gofmt` clean

---

## Why Sway exists

OpenSearch's built-in balancer distributes shard *counts* evenly. It has no opinion on JVM heap pressure or disk saturation, so two nodes can end up carrying most of the query and indexing load while the rest sit idle — the cluster is "balanced" on paper and unbalanced in practice.

Sway closes that gap. It scores every node on a weighted blend of heap, disk, and shard-count signals, moves the largest shards off the hottest nodes first, and stops as soon as it's relieved a configurable fraction of the skew. It does not try to achieve a perfect balance in one shot — it does *just enough*, on a schedule, and lets the cluster settle.

The interesting engineering problem here isn't the scoring model, which is a deliberately simple weighted sum. It's that **this program is allowed to move production data around by itself**, and the entire design is organized around making that safe.

---

## Safety model

> An automation that breaks a cluster is worse than one that does nothing.

**Layer 1 — Default immutability.** `dry_run` is `true` in every factory-default config ([`config.Default()`](internal/config/config.go)). The engine plans and logs every move it *would* make without touching the cluster. A misconfigured deployment cannot accidentally move a shard — live execution is an explicit, deliberate opt-in.

**Layer 2 — Circuit breaker.** Before every cycle, three conditions are checked against the live cluster ([`internal/circuitbreaker`](internal/circuitbreaker/breaker.go)):

| Check | What it measures | Default threshold |
|---|---|---|
| Cluster health | `_cluster/health` status | Must be `green` |
| Avg search latency | Mean query time across data nodes | Must be `< 200 ms` |
| Relocating shards | Migrations already in flight | Must be `0` |

If any single check fails, the circuit opens and the whole cycle is aborted — no partial moves. Every check logs both its measured value and its configured threshold, so an operator reading the log can see exactly why automation did or didn't run. The circuit is re-evaluated fresh every cycle; there's no half-open state and no assumption that a bad condition has cleared itself.

**Layer 3 — Conservative move sizing.** `max_moves_per_cycle` caps relocations per pass (default: 5). `skew_reduction_target` stops planning once the projected improvement hits the configured fraction (default: 25%). The generator doesn't try to solve the whole imbalance in one pass — see [`TargetStateGenerator.Generate`](internal/rebalancer/target.go).

**Layer 4 — Placement guards.** A shard's primary and its replica are never planned onto the same destination node, and a destination is skipped if the move would push its projected disk usage past 90%. Both guards are covered by [tests](internal/rebalancer/target_test.go) — including a case where the *largest* candidate shard has no legal destination and the generator correctly skips it in favor of the next one, rather than giving up on the cycle.

---

## See it work

The fastest way to see the whole loop end-to-end is the built-in simulator — no Docker, no OpenSearch cluster, no config:

```bash
go build -o rebalancer ./cmd/rebalancer
./rebalancer --simulate --cycles 3
```

This is a real run, captured verbatim (`internal/simulation` is deterministic — you'll get the same numbers):

```
======================================================================
  SIMULATED CLUSTER INITIAL STATE
  Cluster: simulated-cluster  |  Nodes: 10  |  Total Shards: 31
======================================================================
  node-0   85.0% JVM   20.8% disk   6 shards   ← hot
  node-1   79.0% JVM   15.1% disk   6 shards   ← hot
  node-2   71.0% JVM   10.7% disk   5 shards
  node-3–9  ...                     2 shards each, all underutilised

CYCLE 1
  [CIRCUIT BREAKER] health=GREEN latency=0.0ms relocating=0 → CLOSED
  Current Skew  : 0.1509
  Projected Skew: 0.1038  (31.2% reduction, target 25% ✓)
  Planned 4 moves (largest first):
    logs-2024[0]/primary     22.00 GiB   node-0 → node-9
    traces-2024[0]/primary   19.00 GiB   node-0 → node-8
    logs-2024[1]/primary     16.00 GiB   node-0 → node-7
    logs-2024[2]/primary     14.00 GiB   node-1 → node-6

CYCLE 2
  Current Skew  : 0.1024 → Projected Skew: 0.0643  (37.2% reduction)
  3 moves planned

CYCLE 3
  Current Skew  : 0.0666 → Projected Skew: 0.0483  (27.4% reduction)
  2 moves planned

SIMULATION COMPLETE — 3 cycles executed.
```

Nine targeted moves take the cluster from a two-hot-node imbalance (skew 0.151) to a tight band around 0.048 — each move chosen to maximise disk-pressure relief per API call, none of them touching a node already at capacity.

The [live demo](https://joshuabvarghese.github.io/Sway/) replays this exact transcript alongside the circuit-breaker state, if you want to watch it without building anything.

### Dry run against a real cluster

```bash
./rebalancer --config config.json --dry-run --once
```

Plans and logs every move the engine would make, touches nothing, exits. This is the correct first step before ever setting `dry_run: false`.

### Full local demo with real OpenSearch

[`demo/`](demo/) has a self-contained walkthrough: `docker-compose up` a 3-node OpenSearch cluster, seed it with real indices, force a visible skew onto one node, watch it in OpenSearch Dashboards, then run Sway against it in dry-run and live mode. See [`demo/README.md`](demo/README.md) for the full steps.

---

## Architecture

```
cmd/rebalancer/
└── main.go                  CLI wiring; selects real vs simulated client

internal/
├── config/       config.go            Cloud-agnostic config; conservative defaults
├── opensearch/   types.go, client.go  API response types + Client interface/HTTP impl
├── agent/        metrics.go, agent.go MonitoringAgent: scrapes APIs, computes hot-scores
├── circuitbreaker/ breaker.go         Safety gate: health + latency + relocating checks
├── rebalancer/   target.go, engine.go Plans minimal moves; orchestrates the full cycle
└── simulation/   simulator.go         Deterministic virtual 10-node cluster
```

```
                   ┌─────────────────────────────────────────────────────┐
                   │                   Engine (per cycle)                │
                   │                                                     │
  OpenSearch API   │  ┌──────────────┐     ClusterSnapshot               │
  (real or sim) ──►│  │  Monitoring  ├─────────────────────►             │
                   │  │    Agent     │                                   │
                   │  └──────────────┘  ┌───────────────────┐            │
                   │                    │  Circuit Breaker  │            │ 
                   │  ClusterSnapshot──►│  • Health check   │            │
                   │                    │  • Latency check  │            │
                   │                    │  • Reloc. check   │            │
                   │                    └────────┬──────────┘            │
                   │                             │ CLOSED                │
                   │                    ┌────────▼──────────┐            │
                   │                    │ Target State Gen. │            │
                   │                    │ • Hot node score  │            │
                   │                    │ • Large-first sort│            │
                   │                    │ • Placement guards│            │
                   │                    └────────┬──────────┘            │
                   │                             │ ShardMoves            │
                   │                    ┌────────▼──────────┐            │
                   │                    │  Executor         │──►  API    │
                   │                    │  _cluster/reroute │  (or log)  │
                   │                    └───────────────────┘            │
                   └─────────────────────────────────────────────────────┘
```

---

## Hot node scoring

```
HotScore = JVMWeight × heapUsed% + DiskWeight × diskUsed% + ShardWeight × (shardCount / maxShards)
```

| Metric | Weight | Rationale |
|---|---|---|
| JVM heap | **0.40** | Heap pressure is the most direct signal of query/indexing load |
| Disk usage | **0.40** | Disk saturation causes the hardest failure mode — writes stop entirely |
| Shard count | **0.20** | A proxy for query fan-out and indexing thread contention |

A node is `HOT` when its score reaches `hot_node_threshold` (default `0.70`; the simulator uses `0.50`, tuned for its smaller synthetic cluster — see `main.go`). Weights and threshold are config, not code.

---

## Cloud-agnostic design

`opensearch.Client` is the only abstraction boundary. The monitoring agent, circuit breaker, target-state generator, and executor are all cloud-neutral and have no idea what's underneath them:

```go
type Client interface {
    GetNodesStats(ctx context.Context) (*NodesStatsResponse, error)
    GetClusterState(ctx context.Context) (*ClusterStateResponse, error)
    GetClusterHealth(ctx context.Context) (*ClusterHealthResponse, error)
    GetShardSizes(ctx context.Context) (map[string]int64, error)
    Reroute(ctx context.Context, req *RerouteRequest, dryRun bool) (*RerouteResponse, error)
}
```

Cloud-specific auth is injected as a `http.RoundTripper` — this is an extension point the codebase supports, not a bundled integration; you provide the transport:

```go
// AWS OpenSearch Service — plug in your own SigV4 signing transport
client := opensearch.NewHTTPClient(
    "https://search-mycluster.us-east-1.es.amazonaws.com",
    "", "", true, 30*time.Second,
    opensearch.WithTransport(yourSigV4RoundTripper),
)

// On-premise — basic auth, no custom transport needed
client := opensearch.NewHTTPClient("https://10.0.0.1:9200", "admin", "secret", true, 30*time.Second)
```

---

## Getting started

**Prerequisites:** Go 1.21+

```bash
git clone https://github.com/joshuabvarghese/sway.git
cd sway
go build -o rebalancer ./cmd/rebalancer
go test ./...          # 20/20, all deterministic, no network required
```

### CLI reference

```
Usage: rebalancer [flags]

  --config string    Path to JSON config file (default: "config.json")
  --simulate         Run against a virtual 10-node cluster (no real cluster needed)
  --dry-run          Plan moves but do not execute (overrides config file)
  --once             Execute a single cycle then exit
  --cycles int       Number of simulation cycles (default: 3, --simulate only)
```

### Configuration

Every field, with its default:

```json
{
  "opensearch": {
    "addresses": ["http://localhost:9200"],
    "username": "", "password": "",
    "tls_verify": true,
    "timeout_seconds": 30
  },
  "agent": {
    "poll_interval_seconds": 30,
    "hot_node_threshold": 0.70,
    "jvm_weight": 0.40, "disk_weight": 0.40, "shard_weight": 0.20
  },
  "rebalancer": {
    "dry_run": true,
    "max_moves_per_cycle": 5,
    "skew_reduction_target": 0.25,
    "large_shard_threshold_bytes": 2147483648
  },
  "circuit_breaker": {
    "required_health": "green",
    "max_avg_latency_ms": 200.0,
    "max_relocating_shards": 0
  }
}
```

`dry_run` defaults to `true`. Sway will not move a single shard until you explicitly set it to `false`.

---

## What's tested, and what isn't

The four packages that make placement decisions or gate execution have real, deterministic unit tests — no live cluster or mocked network calls required:

- **`circuitbreaker`** — every check independently and in combination, health-rank comparisons, and that `Last()` reflects the most recent evaluation.
- **`config`** — the safety-first defaults, that a partial config file overlays correctly onto (rather than replacing) defaults, and that a missing file errors instead of silently defaulting.
- **`rebalancer`** — largest-shard-first ordering, the primary/replica co-location guard, the 90% capacity guard, both cycle-stopping conditions (`max_moves_per_cycle` and `skew_reduction_target`), and the case where the largest candidate has no legal destination and the generator correctly falls through to the next one.

```
go test ./...
ok  github.com/project-sway/sway/internal/circuitbreaker
ok  github.com/project-sway/sway/internal/config
ok  github.com/project-sway/sway/internal/rebalancer
```

What isn't covered yet: `internal/agent` (the scraping/aggregation glue), `internal/opensearch` (the HTTP client — would need a mocked server), and `internal/simulation` (exercised indirectly by every simulator run, including the transcript above, but with no assertions of its own). Honest gap, not hidden.

---

## Extending the engine

**True p99 latency.** The agent currently computes average latency from cumulative OpenSearch counters — a reasonable proxy, not a histogram percentile. To use true p99, implement a collector that reads from Prometheus/OTel/your APM and set `snap.AvgSearchLatMs` accordingly. Nothing in the circuit breaker or rebalancer needs to change.

**A new scoring signal.** Add a field to `agent.NodeMetrics`, populate it in `CollectSnapshot`, add a weight to `config.AgentConfig`, and include the term in both `agent.hotScore` and `rebalancer.projectedNode.hotScore` — they're intentionally kept in lockstep.

**A different execution backend.** Implement `opensearch.Client` — to target a different search engine's reroute API, or to stub moves in an integration test — and hand it to `rebalancer.NewEngine`. Nothing else in the stack changes.

---

## Where this fits

This is one of several infrastructure-tooling projects I've built to explore a specific slice of distributed-systems and SRE engineering — this one is deliberately narrow: a single automation, doing one job, with the majority of the design effort spent on the conditions under which it's *allowed* to act. It isn't a chaos-engineering harness, a general agent framework, or a debugging proxy — those are separate projects in the same portfolio, built around different problems on purpose.

---

## Licence

MIT
