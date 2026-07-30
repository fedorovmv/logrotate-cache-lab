# Log rotation file-cache lab

This repository reproduces the Linux cgroup memory behavior of two external
log-rotation algorithms on disk-backed storage:

- `copytruncate`: copy the active file, `fsync` the backup, then truncate the
  active inode. This is the relevant ordering used by the current
  `pilot-agent` rotator.
- `rename-reopen`: rename the active inode, create its replacement, then ask
  the Envoy-like writer to reopen the log through an HTTP equivalent of
  `/reopen_logs`.
- `baseline` (memory sweep only): write the same logical byte count without
  rotation. It measures service-process and ordinary write-cache background.

The workload deliberately does not support tmpfs. Docker runs use a local
volume; Kubernetes runs use `emptyDir: {}` without `medium: Memory`.

## Architecture

The Go `loglab` binary has orchestration, rotation, monitoring, and host-fallback
writer roles. In Docker and kind, `run` starts a separate C++17 writer with
preallocated record/flush buffers and no garbage collector, plus Go rotator and
monitor subprocesses in the same cgroup. Thus the container has three service
Go runtimes and one C++ writer. Host integration tests use the Go writer when
`LOGLAB_WRITER_EXECUTABLE` is empty. The writer emits fixed-width records with
monotonic sequence IDs. The monitor reads
cgroup v2 `memory.current` and `memory.stat` (with basic cgroup v1 support), and
the harness checks all active and backup files for gaps, duplicates, malformed
records, and ordering violations.

The host-side memory sweep creates a fresh Docker volume and result directory
for every attempt. It runs with equal `--memory` and `--memory-swap` values and
uses repeated aligned binary search to find the lowest passing memory limit.
It first searches the no-rotation baseline with the same process topology and
logical byte count, then reports each strategy's absolute minimum and its
delta from that baseline.

## Prerequisites

- Go 1.26 or newer
- a C++17 compiler for `make build` and the C++ writer integration test
- Docker with Linux containers and cgroup memory accounting
- Bash and Python 3 for the comparison script
- kind and kubectl for the Kubernetes smoke run

On Docker Desktop, counters describe the Linux VM/container cgroup, not macOS
host file cache. Run production measurements on the same kernel, filesystem,
container runtime, and storage class as the affected workload when possible.

## Commands

```bash
make test
make vet
make build
make docker-smoke
./scripts/compare.sh --quick
./scripts/compare.sh
./scripts/compare-50m-5x.sh
./scripts/memory-sweep.sh --quick
./scripts/memory-sweep.sh
./scripts/memory-sweep-50m.sh
./scripts/pressure-boundaries.sh
./scripts/kind-sweep.sh --quick
```

The non-quick Docker sweep defaults to 16–512 MiB, a 4 MiB grid, three
repetitions per candidate, four 32 MiB rotations, 8 MiB/s log traffic, and a
32 MiB touched resident allocation. It can take a long time. The quick mode is
a pipeline check and is not intended to establish a stable production limit.

`memory-sweep-50m.sh` is the larger-file profile: one 50 MiB rotation,
8 MiB/s writer rate, 32 MiB resident allocation, a 4 MiB search grid, and
three required repetitions. Its `--quick` mode uses one repetition and a
16 MiB grid only as a pipeline check.

`compare-50m-5x.sh` tests cache accumulation across five consecutive 50 MiB
rotations with `max-backups=5`, a 20 ms sampling interval, and the same 768 MiB
container limit for both strategies. It writes a time-series SVG next to the
CSV and JSON results.

`pressure-boundaries.sh` checks whether the one-rotation memory boundary still
holds across five retained 50 MiB files, and repeats the same boundary check
with 100 MiB and 200 MiB active files. Every boundary candidate requires three
runs; use `--quick` only to validate the pipeline with one run.

The measured comparison, charts, interpretation, and limitations are in the
human-readable [HTML report](reports/logrotate-cache-report.html). In the
current Docker Desktop environment, the adjacent pressure candidates were
64/68 MiB (fail/pass) for one and five 50 MiB copytruncate rotations, 68/72 MiB
for one 100 MiB copytruncate rotation, and 52/56 MiB for the 100 MiB baseline,
100 MiB rename-reopen, and five 50 MiB rename-reopen rotations. The 200 MiB
profile also resolved at 68/72 MiB for copytruncate and 52/56 MiB for baseline
and rename-reopen. The observed 68/72 MiB difference is one 4 MiB test step and
must not be interpreted as a proven 4 MiB file-size effect.

Generated reports are written below `results/`. The scripts remove only the
containers, named volumes, temporary manifests, and kind cluster that they
created. A terminated script can leave a Docker volume named
`loglab-sweep-*`; it is safe to inspect and remove that specific volume.

## Workload flags

`loglab run` accepts:

| Flag | Default | Meaning |
| --- | ---: | --- |
| `--strategy` | `copytruncate` | `copytruncate` or `rename-reopen` |
| `--log-dir` | `/var/log/loglab` | disk-backed active/backup log directory |
| `--result-dir` | `/results` | CSV and JSON output directory |
| `--max-file-bytes` | 32 MiB | rotation threshold |
| `--rotations` | 4 | completed rotations |
| `--max-backups` | 0 | retained backups; zero retains all |
| `--record-bytes` | 512 | fixed sequence-record size |
| `--bytes-per-second` | 8 MiB/s | target writer rate |
| `--buffer-bytes` | 64 KiB | writer flush threshold |
| `--flush-interval` | 100 ms | maximum buffered time |
| `--monitor-interval` | 100 ms | cgroup sample interval |
| `--resident-bytes` | 32 MiB | anonymous memory touched page-by-page |
| `--monitor` | true | enable cgroup sampling |
| `--writer-executable` | environment | external writer; empty uses the Go fallback |

`loglab memory-sweep` additionally accepts `--lower-mib`, `--upper-mib`,
`--step-mib`, `--repetitions`, `--strategy`, `--image`, `--result-root`, and
`--output`. `--strategy=both` searches the baseline and both algorithms
independently.

## Reports

Every report includes `schema_version`. A run result directory contains:

- `samples.csv`: elapsed time, `memory.current`, file cache, RSS, anonymous
  memory, inactive/active file pages, dirty/writeback bytes, and current log
  sizes;
- `events.csv`: rotation threshold and phase timestamps, including copy sync,
  truncate, rename, replacement creation, and reopen request;
- `integrity.json`: expected and unique sequence counts plus missing,
  duplicate, malformed, wrong-run-ID, and descending counts;
- `summary.json`: peak memory/RSS/anonymous memory, cache distribution and
  rates, writer implementation/state, integrity, cgroup version, and
  acceptance result;
- child stdout/stderr logs for diagnosis.

The main cache fields in `summary.json` are:

- `overall_rate_bytes_per_second`: endpoint cache change divided by duration;
- `positive_rate_bytes_per_second`: accumulated positive cache deltas divided
  by time spent in positive sample-to-sample intervals. It includes short copy
  spikes and must not be interpreted as the slope between rotations or as the
  whole-run average;
- `reclaim_rate_bytes_per_second`: mean magnitude of falling edges;
- `max_bytes` and `p95_bytes`: peak and nearest-rank p95 cache charge;
- `rotation_transient_max_bytes`: maximum cache observed during completed
  rotation intervals.

Sweep attempts explicitly separate `oom`, `functional_passed`,
`integrity_passed`, `failure_kind`, and final `passed`. Exit code 137 is
classified as OOM; other non-zero exits and invalid summaries are functional
failures. `boundary_resolved=false` means the configured lower bound passed, so
the true minimum is below the reported lowest tested value.
`baseline_minimum_mib`, `delta_minimum_mib`, and `delta_resolved` make the
service-process background visible instead of treating it as Envoy memory.

## Integrity semantics

`copytruncate` cannot atomically coordinate with an independent writer. Data
appended after the copy reaches EOF but before truncate is neither in the
backup nor in the post-truncate active file. `fsync` on the backup makes copied
bytes durable but does not close that race. Therefore integrity loss is
reported for `copytruncate` but does not make its memory-limit attempt fail.

For `rename-reopen`, loss, duplication, malformed data, a wrong run ID, a
descending transition, or a failed reopen makes the attempt fail. Rename keeps
the old inode valid until the writer processes reopen, so records written in
that interval remain in the renamed backup.

The Go copy rotator matches the production rotator's significant sequence
`open → io.Copy → backup Sync → active truncate`; the C++ process only emulates
the independent Envoy writer. On Linux the probe in `tools/copyprobe` confirms
that this file-to-file `io.Copy` uses `copy_file_range`, followed by `fsync`.
The lab does not reproduce Envoy's
exact access-log implementation, allocator, or worker/thread scheduling, so it
demonstrates the race and cache cost but does not predict the exact number of
lost production log lines.

## Interpreting file cache and memory limits

Disk-backed file cache is charged to the container cgroup and can remain there
after I/O. It is normally reclaimable, but reclaim is not instantaneous: dirty
pages, writeback rate, active-page aging, filesystem/runtime behavior, and the
container's anonymous working set affect whether the kernel can reclaim enough
before the cgroup reaches its limit.

`copytruncate` reads the active inode and writes the same bytes into a second
inode, so a rotation can temporarily charge source and destination pages and
produce a larger rising/falling cache sawtooth. Retained backup files do not by
themselves reserve RAM, but recently read or written backup pages may stay
cached. `rename-reopen` does not copy file contents: it changes directory
entries and continues with a replacement inode, so it removes the full-file
copy transient. It does not guarantee a flat cache because normal log writes
still populate cache.

The minimum passing limit is an empirical boundary for the selected workload,
not a universal container recommendation. The writer is C++ but is still not
Envoy, while the harness/monitor and standalone rotator add Go runtime memory
that does not map one-to-one to the production sidecar. Do not transfer
`minimum_pass_mib` to an Envoy resource limit. Use `delta_minimum_mib`, cache
counters, and the attempt's
RSS/anonymous memory to compare algorithms; confirm an absolute production
limit with real Envoy on the target platform.

## kind notes and troubleshooting

The kind Job sets equal memory request/limit, `backoffLimit: 0`, non-root
security context, and disk-backed `emptyDir: {}` volumes for logs and results.
The runner saves Pod JSON, stdout, termination reason, and exit code before
deleting its disposable cluster. A Kubernetes OOM normally appears as
`reason=OOMKilled` and `exit=137`.

If no cache samples appear, inspect `monitor.log` and confirm that the runtime
exposes standard cgroup v1/v2 memory files. If a low-limit Docker attempt exits
without `summary.json`, inspect its `error` and `exit_code` in
`memory-sweep.json`; an OOM may kill the harness before it can write reports.
If kind cannot load the image, confirm Docker and kind use the same container
backend and that the configured image name matches `LOGLAB_IMAGE`.
