# Logrotate Cache Lab Design

## 1. Goal

Build a reproducible Linux test lab that compares two pilot-agent-style log
rotation strategies while a separate Envoy-like process continuously writes a
buffered log:

1. `copytruncate`: copy the active file to a backup, sync the backup, then
   truncate the active file;
2. `rename-reopen`: rename the active file, create a replacement, then ask the
   writer to reopen the original path through an HTTP endpoint analogous to
   Envoy's `/reopen_logs`.

The lab must quantify cgroup file-cache behavior, find the minimum viable
container memory limit for each strategy, and validate log sequence integrity.

## 2. Scope

### In scope

- One Go module and one multi-call Linux binary.
- The writer, rotator, and monitor run as separate OS subprocesses in the same
  container and cgroup.
- Disk-backed Docker local volumes and disk-backed Kubernetes `emptyDir`.
- Docker comparison benchmark.
- Docker memory-limit binary search with three successful repetitions required
  at the winning limit.
- Optional kind Job runner using the same binary and report format.
- CSV time series and JSON summary reports.
- Sequence-ID analysis for missing, duplicated, malformed, and out-of-order
  records.
- Linux cgroup v2 metrics, with a cgroup v1 compatibility reader where the
  equivalent counters are available.

### Out of scope

- `tmpfs`, `emptyDir.medium: Memory`, or swap experiments.
- Running a real Envoy binary.
- Modifying Istio or Envoy repositories.
- Prometheus, Grafana, or a long-running service deployment.
- Claiming that a result measured on Docker Desktop is an absolute production
  memory requirement; results describe the selected workload and runtime.

## 3. Design status

**Gate: READY.**

The user selected disk-backed storage, requested integrity validation, and
approved a three-run binary search for minimum memory. No open decision changes
component boundaries or report semantics.

## 4. Architecture

The image contains one binary named `loglab`. Its first argument selects a
role:

```text
loglab run
├── loglab writer
├── loglab rotate --strategy=copytruncate|rename-reopen
└── loglab monitor
```

`loglab run` is the in-container orchestrator. It creates a run directory,
starts the three child processes, propagates cancellation, waits for clean
shutdown, performs final integrity analysis, and writes the JSON summary.

The host scripts are deliberately thin:

- `scripts/compare.sh` runs both strategies with fresh Docker volumes;
- `scripts/memory-sweep.sh` repeatedly invokes `docker run` with memory and
  swap set to the same value;
- `scripts/kind-sweep.sh` optionally performs the same search using Kubernetes
  Jobs.

The Docker daemon, not the container, owns orchestration of memory-limited
runs. If the cgroup is OOM-killed, the host script can still observe exit code
137 and continue the search.

## 5. Writer: Envoy emulator

The writer opens the configured active path with create, append, and write
flags. It produces fixed-size newline-terminated records at a configured byte
rate. Each record contains:

```text
<run-id> <20-digit-sequence> <payload-padding>\n
```

Sequence numbers begin at one and increase exactly once per generated record.
The fixed width makes the configured byte rate and final expected byte count
deterministic.

The writer maintains an in-memory buffer and flushes when either:

- the buffer reaches a configured threshold; or
- the configured flush interval elapses.

It exposes an HTTP server on loopback:

- `POST /reopen-logs` sets a reopen request and wakes the flush loop;
- `GET /healthz` reports readiness;
- `GET /state` reports generated and successfully written record counts.

When processing reopen, the flush loop atomically takes the buffered batch,
closes the old descriptor, opens the original active path, then writes the
batch to the new descriptor. This mirrors the relevant ordering in Envoy's
`AccessLogFileImpl`: pending data is captured, the file is closed and reopened,
and the captured data is written afterward.

If reopen fails, the writer increments a failure counter, returns a non-zero
final status, and preserves the error in the run report. The lab does not hide
this as a successful integrity result.

## 6. Rotation implementations

Both strategies share threshold detection, backup naming, retention, event
recording, and error reporting. Backup names contain the rotation ordinal, so
they cannot overwrite each other when multiple rotations happen within one
second.

### 6.1 `copytruncate`

When the active file reaches `max-file-bytes`:

1. open the active file for reading;
2. create a new backup with exclusive creation;
3. copy until EOF;
4. sync and close the backup;
5. truncate the active path to zero without asking the writer to reopen;
6. enforce retention.

The writer continues writing throughout the copy. This intentionally preserves
the same data-loss window as the current pilot-agent implementation.

### 6.2 `rename-reopen`

When the active file reaches `max-file-bytes`:

1. stat the active file and retain mode, UID, and GID;
2. atomically rename it to a unique backup on the same filesystem;
3. create a replacement active file with exclusive creation;
4. restore mode, UID, and GID;
5. call the writer's `POST /reopen-logs` endpoint;
6. enforce retention.

Writes made before the writer processes reopen continue through the old file
descriptor into the renamed backup. Buffered writes captured during reopen go
to the replacement file. A reopen failure invalidates the run.

## 7. Storage isolation

Docker benchmark runs use a fresh Docker local volume mounted at `/var/log/loglab`.
The log workload never uses a macOS bind mount, because host sharing layers can
change cache accounting. A small result directory may be bind-mounted at
`/results`; report writes are excluded from log-size calculations.

The kind Job uses:

```yaml
volumes:
  - name: logs
    emptyDir: {}
```

No storage mode in this project mounts a memory-backed filesystem.

## 8. Measurements

The monitor samples at a configurable interval, defaulting to 100 ms. Every
sample contains:

- monotonic timestamp and elapsed nanoseconds;
- strategy and run ID;
- `memory.current`;
- cgroup file-cache bytes;
- `inactive_file`, `active_file`, `file_dirty`, `file_writeback`, and `shmem`;
- aggregate RSS for the orchestrator and child processes;
- active log bytes;
- backup bytes and backup count;
- latest rotation phase and ordinal.

For cgroup v2, cache is the `file` field in `memory.stat`. For cgroup v1, cache
uses the best available `total_cache` or `cache` counter and the report records
which source was selected. Missing nonessential counters are emitted as null,
not silently converted to zero.

Rotation events are written separately with timestamps for:

- threshold reached;
- copy or rename start;
- copy sync complete;
- truncate complete;
- reopen requested;
- retention complete;
- rotation failed.

## 9. Derived metrics

The analyzer emits, in bytes and MiB where appropriate:

- start, end, maximum, mean, median, and p95 cache;
- maximum `memory.current` and RSS;
- maximum dirty and writeback cache;
- overall cache delta divided by elapsed time;
- average positive cache-fill rate;
- least-squares cache slope for each interval between completed rotations;
- maximum transient cache increase between rotation start and completion;
- cache drop after each completed rotation;
- writer throughput and rotation duration statistics.

The headline `cache_fill_rate_mib_per_sec` is the mean of positive per-sample
cache deltas divided by their elapsed time. Negative reclaim intervals are
reported separately and do not cancel the fill rate. This definition makes a
sawtooth workload comparable between strategies.

## 10. Integrity analysis

After the writer is stopped and flushed, the analyzer scans the active file and
all retained backups.

It reports:

- records generated by the writer;
- valid unique sequence IDs found;
- missing IDs;
- duplicate IDs;
- malformed or partial records;
- descending sequence transitions within each physical file;
- minimum and maximum sequence IDs per file.

The comparison does not hard-code that `copytruncate` must lose data. It records
the observed result. `rename-reopen` is successful only when it reports no
missing, duplicate, or malformed records and no reopen failure.

To avoid confusing retention deletion with rotation loss, integrity benchmark
runs retain every backup produced during the run. Separate retention unit tests
verify backup deletion behavior.

## 11. Minimum-memory search

The host runner accepts a lower bound, upper bound, resolution, and repetition
count. Defaults are:

- resolution: 4 MiB;
- repetitions: 3;
- swap disabled by setting `--memory-swap` equal to `--memory`;
- fresh local volume for every attempt.

For each strategy it performs an aligned binary search. A limit passes only if
all repetitions:

- exit with status zero;
- complete the configured number of rotations;
- produce a valid JSON summary;
- meet that strategy's integrity acceptance rule;
- report no reopen failure.

Exit 137 is recorded as OOM. Other non-zero exits are recorded as functional
failures and are not mislabelled as memory thresholds.

The output includes the minimum passing limit, greatest failing limit, all
attempts, and the workload parameters. It also reports the conservative model:

```text
peak RSS + active-file target + observed rotation overhead
```

The measured minimum remains specific to the configured record rate, file
size, retention, filesystem, kernel, cgroup version, and Docker or Kubernetes
runtime.

## 12. Configuration and defaults

Every workload parameter is a CLI flag and appears in the report. Fast defaults
keep a local comparison under several minutes:

- record size: 512 bytes;
- write rate: 8 MiB/s;
- writer buffer: 64 KiB;
- flush interval: 100 ms;
- active-file threshold: 32 MiB;
- rotations: 4;
- backups retained during integrity runs: unlimited for the bounded run;
- monitor interval: 100 ms;
- emulated resident allocation: 32 MiB;
- memory sweep range: 64-512 MiB;
- memory sweep resolution: 4 MiB;
- memory sweep repetitions: 3.

The resident allocation is touched page-by-page and retained for the run. It
models Envoy's non-cache working set and prevents meaningless limits based only
on the small Go processes.

## 13. Reports

Each run writes:

```text
results/<run-id>/samples.csv
results/<run-id>/events.csv
results/<run-id>/summary.json
results/<run-id>/integrity.json
```

The comparison script writes `results/comparison.json` and prints a compact
table. The memory sweep writes `results/memory-sweep.json` and prints one row
per strategy containing minimum passing MiB, greatest failing MiB, peak cache,
peak memory, and integrity counts.

Reports include schema version, binary version, Go version, kernel, cgroup
version, filesystem type, Docker or Kubernetes runtime, workload flags, and
strategy.

## 14. Testing strategy

Development follows test-first red-green cycles.

### Unit tests

- fixed-size record encoding and parsing;
- cache counter parsing for representative cgroup v1 and v2 fixtures;
- slope, percentile, positive-fill-rate, and reclaim calculations;
- unique backup naming;
- retention ordering;
- integrity gap, duplicate, malformed, and order detection;
- aligned binary-search decisions and three-run pass rule.

### Process integration tests

- writer writes and flushes records;
- `/reopen-logs` moves subsequent writes to a replacement inode;
- `copytruncate` produces a backup and leaves the active descriptor usable;
- `rename-reopen` produces a backup and preserves all generated sequence IDs;
- orchestrator terminates child processes and writes reports.

### Docker acceptance tests

- build the image;
- run both strategies against fresh disk-backed volumes;
- validate both report schemas;
- validate cache samples and rotation events are nonempty;
- run a reduced memory sweep and show an ordered failing/passing boundary;
- verify all test containers and temporary volumes are removed by the scripts.

### Kind acceptance test

- load the image into a disposable kind cluster;
- run one Job per strategy with a disk-backed `emptyDir` and a memory limit;
- retrieve and validate summaries from Job logs;
- delete the disposable cluster.

## 15. Error handling and cleanup

- Child startup failures stop the run and preserve diagnostics.
- The orchestrator sends graceful termination, waits for a bounded interval,
  then kills only its known child processes.
- Rotation errors are fatal for that run.
- Reports use temporary files followed by rename so a partial report is not
  accepted as success.
- Host scripts use run-specific container and volume names and trap normal
  shell exit to clean them. They never remove broad or unresolved paths.
- OOM-killed runs may lack an in-container summary; the host attempt record is
  the authoritative result for that case.

## 16. Acceptance criteria

The project is complete when:

1. `go test ./...` passes on the host;
2. the Docker image builds from a clean checkout;
3. `scripts/compare.sh` completes both strategies and produces CSV and JSON;
4. both strategies record cache-fill speed, peak cache, peak memory, and
   rotation duration;
5. integrity reports quantify loss and duplication without conflating deleted
   retention files;
6. `scripts/memory-sweep.sh` finds and verifies a three-run passing boundary for
   both strategies;
7. `scripts/kind-sweep.sh` can run the same workload in a disposable kind
   cluster;
8. README commands explain prerequisites, runtime, output, interpretation, and
   why results vary across kernels and filesystems.

## 17. Unknowns registry

| ID | Unknown | Priority | Resolution or guardrail | Status |
|---|---|---:|---|---|
| U-01 | Docker Desktop file-cache behavior differs from production Linux nodes | P1 | Record kernel, filesystem, runtime, and cgroup version; provide kind runner; do not generalize numeric thresholds | Bounded |
| U-02 | OOM runs cannot always flush an in-container report | P1 | Host runner owns attempt records and uses container exit 137 as authoritative | Closed |
| U-03 | Cache counters differ between cgroup v1 and v2 | P1 | Detect version and record counter source; null unsupported fields | Closed |
| U-04 | Retention can masquerade as data loss | P1 | Retain all backups during integrity runs; test retention separately | Closed |
| U-05 | Reopen HTTP success precedes actual asynchronous reopen | P1 | Writer exposes final reopen failure count; precreate replacement before request; run fails on reopen error | Closed |
| U-06 | Very fast filesystems may make copy peaks shorter than sampling interval | P2 | Default 100 ms sampling, event timestamps, configurable interval, and rotation transient metric | Bounded |

## 18. Contract for implementation

Implement the accepted subprocess boundaries, report definitions, integrity
rules, and disk-backed-only constraint. Keep workload parameters explicit in
every report.

If implementation reveals a fact that changes cgroup counter meaning,
`/reopen-logs` ordering, integrity accounting, or the host-owned OOM decision,
stop implementation, record the evidence, and return the design for review.
Do not silently change report semantics to accommodate an unexpected runtime.
