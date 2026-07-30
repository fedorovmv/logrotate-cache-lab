# Logrotate Cache Lab Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Docker- and kind-runnable Go laboratory that compares copytruncate with rename/reopen using subprocesses, cgroup cache sampling, integrity analysis, and a repeated minimum-memory search.

**Architecture:** A single `loglab` multi-call binary runs as orchestrator, writer, rotator, monitor, analyzer, or memory-sweep host driver. The in-container orchestrator starts writer, rotator, and monitor subprocesses in one cgroup; host scripts create fresh disk-backed volumes and own OOM decisions.

**Tech Stack:** Go 1.26 standard library, Linux cgroup v1/v2 files, Docker CLI, kind, Kubernetes Jobs, CSV and JSON.

---

## File map

```text
go.mod                                      Go module
cmd/loglab/main.go                          multi-call CLI and flag parsing
internal/record/record.go                   fixed-width sequence records
internal/record/record_test.go              record red/green tests
internal/cgroup/reader.go                   cgroup discovery and samples
internal/cgroup/reader_test.go              v1/v2 fixture tests
internal/metrics/analyze.go                 slopes, percentiles, fill/reclaim
internal/metrics/analyze_test.go            deterministic metric tests
internal/report/report.go                   report schemas and atomic JSON/CSV
internal/writer/writer.go                   buffered writer and HTTP reopen
internal/writer/writer_test.go               inode-switch integration test
internal/rotator/rotator.go                  shared threshold/retention loop
internal/rotator/copytruncate.go             current Istio-like algorithm
internal/rotator/renamereopen.go             rename/create/reopen algorithm
internal/rotator/rotator_test.go             strategy and retention tests
internal/integrity/analyze.go                sequence-set analysis
internal/integrity/analyze_test.go           gap/duplicate/malformed tests
internal/harness/run.go                      subprocess orchestration
internal/harness/run_test.go                 end-to-end process test
internal/sweep/search.go                     aligned repeated binary search
internal/sweep/search_test.go                boundary decision tests
internal/sweep/docker.go                     Docker attempt execution
Dockerfile                                  Linux image
Makefile                                    repeatable local commands
scripts/compare.sh                           disk-backed strategy comparison
scripts/memory-sweep.sh                      Docker minimum-memory search
scripts/kind-sweep.sh                        disposable kind Job smoke sweep
deploy/job.yaml                              kind Job template
README.md                                    operation and interpretation
```

### Task 1: Module, records, and report contracts

**Files:**
- Create: `go.mod`
- Create: `internal/record/record_test.go`
- Create: `internal/record/record.go`
- Create: `internal/report/report.go`

- [ ] **Step 1: Write the failing fixed-record tests**

```go
func TestEncodeParseFixedRecord(t *testing.T) {
    got, err := Encode("run-a", 42, 128)
    if err != nil { t.Fatal(err) }
    if len(got) != 128 { t.Fatalf("length=%d", len(got)) }
    parsed, err := Parse(got)
    if err != nil { t.Fatal(err) }
    if parsed.RunID != "run-a" || parsed.Sequence != 42 {
        t.Fatalf("parsed=%+v", parsed)
    }
}

func TestEncodeRejectsRecordTooSmall(t *testing.T) {
    if _, err := Encode("run-a", 1, 8); err == nil {
        t.Fatal("expected size error")
    }
}
```

- [ ] **Step 2: Run the record tests and verify RED**

Run: `go test ./internal/record -run 'TestEncode' -v`

Expected: build failure because `Encode` and `Parse` do not exist.

- [ ] **Step 3: Implement the fixed-width codec**

Define:

```go
type Record struct {
    RunID    string
    Sequence uint64
}

func Encode(runID string, sequence uint64, size int) ([]byte, error)
func Parse(line []byte) (Record, error)
```

Use the prefix `runID + " " + fmt.Sprintf("%020d", sequence) + " "`, fill the
remaining bytes with `x`, and reserve the final byte for `\n`. Reject whitespace
in the run ID and sizes that cannot fit the prefix.

- [ ] **Step 4: Add versioned report types**

Define `Sample`, `RotationEvent`, `IntegrityReport`, `RunSummary`,
`ComparisonReport`, `SweepAttempt`, and `SweepReport`. Use integer byte fields,
nanosecond timestamps, nullable pointers for unsupported cgroup counters, and a
constant `SchemaVersion = 1`. Add `WriteJSONAtomic(path string, value any)` that
writes a temporary sibling, syncs it, closes it, and renames it.

- [ ] **Step 5: Run tests and commit**

Run: `go test ./internal/record ./internal/report -v`

Expected: PASS.

Commit:

```bash
git add go.mod internal/record internal/report
git commit -m "feat: define records and report schemas"
```

### Task 2: Cgroup and cache metric calculations

**Files:**
- Create: `internal/cgroup/reader_test.go`
- Create: `internal/cgroup/reader.go`
- Create: `internal/metrics/analyze_test.go`
- Create: `internal/metrics/analyze.go`

- [ ] **Step 1: Write failing cgroup fixture tests**

Use temporary directories containing representative v2 files:

```text
memory.current = 104857600
memory.stat:
anon 33554432
file 67108864
inactive_file 50331648
active_file 16777216
file_dirty 4194304
file_writeback 1048576
shmem 0
```

Assert `Reader.Sample()` returns the exact counters and identifies `VersionV2`.
Add a v1 fixture with `memory.usage_in_bytes`, `memory.stat cache`,
`total_cache`, `inactive_file`, and `active_file`; assert `total_cache` wins.

- [ ] **Step 2: Verify cgroup tests fail**

Run: `go test ./internal/cgroup -v`

Expected: build failure because `Reader` does not exist.

- [ ] **Step 3: Implement detection and parsing**

Define:

```go
type Version string
const (VersionV1 Version = "v1"; VersionV2 Version = "v2")

type MemorySample struct {
    Current, Cache uint64
    Anon, InactiveFile, ActiveFile, Dirty, Writeback, Shmem *uint64
    Version Version
    CacheSource string
}

func Discover() (*Reader, error)
func NewReader(root string, version Version) *Reader
func (r *Reader) Sample() (MemorySample, error)
func (r *Reader) RSSFromCgroupProcs() (uint64, error)
```

`Discover` checks `/sys/fs/cgroup/memory.current` first, then standard v1
locations. `RSSFromCgroupProcs` sums `VmRSS` from `/proc/<pid>/status` for PIDs
listed in the cgroup.

- [ ] **Step 4: Write failing derived-metric tests**

Given samples `(0s,10)`, `(1s,20)`, `(2s,15)`, `(3s,35)`, assert:

- overall slope is `(35-10)/3` bytes/s;
- positive fill rate is `(10+20)/2` bytes/s;
- reclaim rate is `5/1` bytes/s;
- maximum is 35;
- p95 uses the nearest-rank definition and is 35.

- [ ] **Step 5: Implement metric analysis and verify GREEN**

Define `Analyze(samples []report.Sample, events []report.RotationEvent) report.CacheMetrics`.
Use elapsed monotonic nanoseconds, guard zero-duration pairs, calculate
least-squares slopes per completed rotation interval, and expose rotation
transient maxima.

Run: `go test ./internal/cgroup ./internal/metrics -v`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/cgroup internal/metrics internal/report
git commit -m "feat: sample and analyze cgroup file cache"
```

### Task 3: Buffered Envoy-like writer and reopen endpoint

**Files:**
- Create: `internal/writer/writer_test.go`
- Create: `internal/writer/writer.go`

- [ ] **Step 1: Write a failing inode-switch integration test**

Start a writer against `active.log`, wait for records, rename `active.log` to
`backup.log`, create a replacement, call `POST /reopen-logs`, and wait for more
records. Stop cleanly and assert:

- both files contain valid records;
- backup inode equals the original inode;
- replacement inode differs;
- the combined sequence set has no gaps or duplicates;
- reopen failure count is zero.

- [ ] **Step 2: Verify writer test RED**

Run: `go test ./internal/writer -run TestReopenSwitchesInodeWithoutLoss -v`

Expected: build failure because `New` and `Run` do not exist.

- [ ] **Step 3: Implement writer configuration and state**

```go
type Config struct {
    RunID, Path, ListenAddress, StatePath string
    RecordBytes, BufferBytes int
    BytesPerSecond int64
    FlushInterval time.Duration
    ResidentBytes int64
}

type State struct {
    Generated, Written, ReopenRequests, ReopenFailures uint64
    LastError string
}

func New(cfg Config) (*Writer, error)
func (w *Writer) Run(ctx context.Context) error
func (w *Writer) State() State
```

Touch resident allocation page-by-page. Use a ticker-derived record budget,
buffer complete records, and make close/open/write ordering single-threaded in
the flush loop. `/healthz`, `/state`, and `/reopen-logs` use loopback HTTP.

- [ ] **Step 4: Verify writer tests and commit**

Run: `go test ./internal/writer -v`

Expected: PASS.

```bash
git add internal/writer
git commit -m "feat: emulate buffered Envoy file logging"
```

### Task 4: Copytruncate and rename/reopen rotators

**Files:**
- Create: `internal/rotator/rotator_test.go`
- Create: `internal/rotator/rotator.go`
- Create: `internal/rotator/copytruncate.go`
- Create: `internal/rotator/renamereopen.go`

- [ ] **Step 1: Write failing copytruncate behavior tests**

Create an active file with known bytes, rotate once, and assert the backup has
the bytes and the active inode is unchanged with size zero. Assert the event
sequence includes `threshold`, `copy-start`, `copy-synced`, `truncate`, and
`retention-complete`.

- [ ] **Step 2: Write failing rename/reopen behavior tests**

Use an `httptest.Server` to count reopen calls. Rotate once and assert:

- backup inode equals the pre-rotation inode;
- replacement inode differs;
- mode is preserved;
- exactly one reopen request occurs;
- event order is `threshold`, `rename`, `replacement-created`,
  `reopen-requested`, `retention-complete`.

Add a retention test with four backups and `MaxBackups=2`; assert only the two
newest ordinals remain.

- [ ] **Step 3: Verify rotator tests RED**

Run: `go test ./internal/rotator -v`

Expected: build failure because strategy implementations do not exist.

- [ ] **Step 4: Implement shared rotation loop**

```go
type Strategy string
const (
    CopyTruncate Strategy = "copytruncate"
    RenameReopen Strategy = "rename-reopen"
)

type Config struct {
    Strategy Strategy
    ActivePath, ReopenURL, EventPath string
    MaxFileBytes int64
    Rotations, MaxBackups int
    PollInterval time.Duration
}

func Run(ctx context.Context, cfg Config) error
func RotateOnce(ctx context.Context, cfg Config, ordinal int) error
```

Use exclusive backup creation, `io.Copy`, `Sync`, and `Truncate` for copy mode.
Use same-filesystem `os.Rename`, replacement creation, chmod/chown from
`syscall.Stat_t`, and an HTTP client with timeout for rename mode. All errors
include strategy, path, ordinal, and phase.

- [ ] **Step 5: Verify tests and commit**

Run: `go test ./internal/rotator -v`

Expected: PASS.

```bash
git add internal/rotator
git commit -m "feat: implement both rotation strategies"
```

### Task 5: Integrity analyzer and subprocess harness

**Files:**
- Create: `internal/integrity/analyze_test.go`
- Create: `internal/integrity/analyze.go`
- Create: `internal/harness/run_test.go`
- Create: `internal/harness/run.go`
- Create: `cmd/loglab/main.go`

- [ ] **Step 1: Write failing integrity tests**

Build fixture files containing sequences `1,2,4`, duplicate `2`, one malformed
line, and a per-file descent `9,8`. Assert exact missing, duplicate, malformed,
and descending-transition counts.

- [ ] **Step 2: Verify integrity RED and implement analyzer**

Run: `go test ./internal/integrity -v`

Expected: build failure because `Analyze` does not exist.

Implement:

```go
func Analyze(runID string, expected uint64, paths []string) (report.IntegrityReport, error)
```

Scan with a buffer large enough for configured records, reject a mismatched run
ID, count all occurrences, sort missing IDs, and retain bounded examples while
keeping full counts.

- [ ] **Step 3: Write failing harness process test**

Build the current test binary as a helper subprocess, run a short two-rotation
rename workload, and assert `samples.csv`, `events.csv`, `summary.json`, and
`integrity.json` exist and integrity is clean.

- [ ] **Step 4: Implement CLI roles and harness**

The CLI supports:

```text
loglab writer [flags]
loglab rotate [flags]
loglab monitor [flags]
loglab run [flags]
loglab analyze [flags]
loglab memory-sweep [flags]
```

`run` creates result paths, starts its own executable for each child, waits for
writer readiness, waits for the requested rotation count, sends SIGTERM to
writer and monitor, reads child state, performs integrity analysis, and writes
atomic reports. Every spawned PID is retained explicitly; cleanup never uses a
name-based process kill.

- [ ] **Step 5: Verify all host tests and commit**

Run: `go test ./... -count=1`

Expected: PASS.

```bash
git add cmd internal/integrity internal/harness
git commit -m "feat: orchestrate subprocess benchmark runs"
```

### Task 6: Docker image and strategy comparison

**Files:**
- Create: `Dockerfile`
- Create: `.dockerignore`
- Create: `scripts/compare.sh`
- Create: `Makefile`

- [ ] **Step 1: Add a failing Docker smoke target**

Create `Makefile` target `docker-smoke` that builds `logrotate-cache-lab:dev`,
runs a reduced rename workload on a fresh named volume, and validates
`summary.json` with `loglab analyze`. Run it before Dockerfile creation.

Run: `make docker-smoke`

Expected: FAIL because `Dockerfile` does not exist.

- [ ] **Step 2: Implement the Linux image**

Use `golang:1.26-bookworm` as builder with `CGO_ENABLED=0`, then
`debian:bookworm-slim` as runtime. Run as a non-root UID/GID, create writable
`/results` and `/var/log/loglab`, and set `ENTRYPOINT ["/usr/local/bin/loglab"]`.

- [ ] **Step 3: Implement comparison script**

`scripts/compare.sh` builds once and runs each strategy with a unique local
volume and result directory. It traps exit to remove only recorded containers
and volumes. It invokes `loglab compare-reports` to write
`results/comparison.json` and print strategy, cache fill MiB/s, peak cache MiB,
peak memory MiB, rotation p95, missing, and duplicate counts.

- [ ] **Step 4: Run Docker acceptance and commit**

Run:

```bash
make docker-smoke
./scripts/compare.sh --quick
```

Expected: both strategies exit zero; rename integrity is clean; both summaries
contain nonempty samples and rotation events.

```bash
git add Dockerfile .dockerignore Makefile scripts/compare.sh
git commit -m "feat: run strategy comparison in Docker"
```

### Task 7: Repeated minimum-memory search

**Files:**
- Create: `internal/sweep/search_test.go`
- Create: `internal/sweep/search.go`
- Create: `internal/sweep/docker.go`
- Modify: `cmd/loglab/main.go`
- Create: `scripts/memory-sweep.sh`

- [ ] **Step 1: Write failing aligned-search tests**

For lower 64, upper 128, step 4, and a fake runner that passes at 92, assert
attempted limits remain aligned, the result is `greatestFail=88` and
`minimumPass=92`, and every candidate requires three successful repetitions.
Add a case where one of three repetitions fails and the candidate is rejected.

- [ ] **Step 2: Verify RED and implement search**

Run: `go test ./internal/sweep -run TestSearch -v`

Expected: build failure because `Search` does not exist.

Define:

```go
type AttemptRunner func(context.Context, report.SweepAttempt) report.SweepAttempt
type Config struct { LowerMiB, UpperMiB, StepMiB, Repetitions int }
func Search(ctx context.Context, cfg Config, run AttemptRunner) (report.SweepReport, error)
```

Round candidates down to the configured grid, reject invalid ranges, stop when
adjacent grid points are known, and preserve every repetition result.

- [ ] **Step 3: Implement Docker attempt runner**

Invoke `docker run --rm --memory=<N>m --memory-swap=<N>m` with a fresh volume
and a unique host result directory. Classify exit 137 as OOM, any other non-zero
exit as functional failure, and a zero exit as pass only after parsing and
validating the summary and integrity report.

- [ ] **Step 4: Add host wrapper and verify reduced sweep**

`scripts/memory-sweep.sh` builds the image and calls `loglab memory-sweep` for
both strategies. Support `--quick` with 64-192 MiB, 16 MiB step, one rotation,
and one repetition for smoke testing while retaining production defaults of
three repetitions and 4 MiB.

Run: `./scripts/memory-sweep.sh --quick`

Expected: JSON contains a nonempty ordered attempt list for both strategies and
at least one passing limit each.

- [ ] **Step 5: Commit**

```bash
git add internal/sweep cmd/loglab/main.go scripts/memory-sweep.sh
git commit -m "feat: find minimum Docker memory limits"
```

### Task 8: Kind runner, documentation, and final verification

**Files:**
- Create: `deploy/job.yaml`
- Create: `scripts/kind-sweep.sh`
- Create: `README.md`
- Modify: `Makefile`

- [ ] **Step 1: Add kind Job template and disposable runner**

The Job mounts `emptyDir: {}` at `/var/log/loglab`, sets an explicit memory
request and limit, disables retries with `backoffLimit: 0`, and writes the JSON
summary to stdout after the run. `kind-sweep.sh` creates a run-specific cluster,
loads the image, applies one Job per strategy, waits with a timeout, records pod
termination reason/exit code/logs, and traps cluster deletion.

- [ ] **Step 2: Document exact commands and interpretation**

README sections:

- prerequisites and architecture;
- `make test`, `make docker-smoke`, comparison, memory sweep, and kind commands;
- all workload flags and defaults;
- report field definitions;
- explanation that disk file cache can grow and remain reclaimable;
- distinction between short copy peak and retained backup cache;
- integrity semantics;
- cgroup/runtime/filesystem limitations;
- cleanup and troubleshooting.

- [ ] **Step 3: Run formatting and host tests**

Run:

```bash
gofmt -w cmd internal
go vet ./...
go test ./... -count=1
```

Expected: all commands exit zero with no warnings.

- [ ] **Step 4: Run Docker comparison and reduced sweep**

Run:

```bash
./scripts/compare.sh --quick
./scripts/memory-sweep.sh --quick
```

Expected: both exit zero and generate versioned JSON and nonempty CSV reports.

- [ ] **Step 5: Run kind smoke test**

Run: `./scripts/kind-sweep.sh --quick`

Expected: both Jobs complete or the script reports an evidence-backed OOM
boundary; the disposable cluster is deleted.

- [ ] **Step 6: Verify repository state and commit**

Run:

```bash
git diff --check
git status --short
git log --oneline --decorate -10
```

Commit:

```bash
git add deploy scripts/kind-sweep.sh README.md Makefile
git commit -m "docs: add kind workflow and lab guide"
```

## Plan self-review

- Spec coverage: writer, both rotators, cache sampling, derived rates, integrity,
  Docker comparison, repeated memory search, kind, reports, and documentation
  all map to explicit tasks.
- Placeholder scan: the plan contains no deferred implementation steps.
- Type consistency: report types originate in Task 1; cgroup and metrics consume
  them in Task 2; writer and rotator contracts precede harness use; sweep runner
  consumes the same report schema.
- Scope: tmpfs is absent; real Envoy and Istio changes remain excluded.
