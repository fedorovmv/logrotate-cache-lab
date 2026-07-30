package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const SchemaVersion = 1

type Sample struct {
	ElapsedNS     int64   `json:"elapsed_ns"`
	Timestamp     string  `json:"timestamp"`
	Strategy      string  `json:"strategy"`
	RunID         string  `json:"run_id"`
	MemoryCurrent uint64  `json:"memory_current"`
	Cache         uint64  `json:"cache"`
	RSS           uint64  `json:"rss"`
	Anon          *uint64 `json:"anon,omitempty"`
	InactiveFile  *uint64 `json:"inactive_file,omitempty"`
	ActiveFile    *uint64 `json:"active_file,omitempty"`
	Dirty         *uint64 `json:"file_dirty,omitempty"`
	Writeback     *uint64 `json:"file_writeback,omitempty"`
	Shmem         *uint64 `json:"shmem,omitempty"`
	ActiveBytes   uint64  `json:"active_bytes"`
	BackupBytes   uint64  `json:"backup_bytes"`
	BackupCount   int     `json:"backup_count"`
}

type RotationEvent struct {
	ElapsedNS int64  `json:"elapsed_ns"`
	Timestamp string `json:"timestamp"`
	Ordinal   int    `json:"ordinal"`
	Phase     string `json:"phase"`
	Bytes     int64  `json:"bytes"`
	Error     string `json:"error,omitempty"`
}

type CacheMetrics struct {
	StartBytes                 uint64    `json:"start_bytes"`
	EndBytes                   uint64    `json:"end_bytes"`
	MaxBytes                   uint64    `json:"max_bytes"`
	MeanBytes                  float64   `json:"mean_bytes"`
	MedianBytes                uint64    `json:"median_bytes"`
	P95Bytes                   uint64    `json:"p95_bytes"`
	OverallRateBytesPerSecond  float64   `json:"overall_rate_bytes_per_second"`
	PositiveRateBytesPerSecond float64   `json:"positive_rate_bytes_per_second"`
	ReclaimRateBytesPerSecond  float64   `json:"reclaim_rate_bytes_per_second"`
	RotationTransientMaxBytes  uint64    `json:"rotation_transient_max_bytes"`
	RotationIntervalSlopes     []float64 `json:"rotation_interval_slopes"`
}

type IntegrityReport struct {
	SchemaVersion         int                `json:"schema_version"`
	Expected              uint64             `json:"expected"`
	ValidUnique           uint64             `json:"valid_unique"`
	Missing               uint64             `json:"missing"`
	Duplicates            uint64             `json:"duplicates"`
	Malformed             uint64             `json:"malformed"`
	WrongRunID            uint64             `json:"wrong_run_id"`
	DescendingTransitions uint64             `json:"descending_transitions"`
	MissingExamples       []uint64           `json:"missing_examples,omitempty"`
	Files                 map[string]FileSet `json:"files,omitempty"`
}

type FileSet struct {
	Records uint64 `json:"records"`
	Min     uint64 `json:"min"`
	Max     uint64 `json:"max"`
}

type WriterState struct {
	Generated      uint64 `json:"generated"`
	Written        uint64 `json:"written"`
	ReopenRequests uint64 `json:"reopen_requests"`
	ReopenFailures uint64 `json:"reopen_failures"`
	LastError      string `json:"last_error,omitempty"`
}

type WorkloadConfig struct {
	MaxFileBytes      int64 `json:"max_file_bytes"`
	Rotations         int   `json:"rotations"`
	MaxBackups        int   `json:"max_backups"`
	RecordBytes       int   `json:"record_bytes"`
	BytesPerSecond    int64 `json:"bytes_per_second"`
	BufferBytes       int   `json:"buffer_bytes"`
	FlushIntervalNS   int64 `json:"flush_interval_ns"`
	MonitorIntervalNS int64 `json:"monitor_interval_ns"`
	ResidentBytes     int64 `json:"resident_bytes"`
}

type RunSummary struct {
	SchemaVersion int             `json:"schema_version"`
	RunID         string          `json:"run_id"`
	Strategy      string          `json:"strategy"`
	Success       bool            `json:"success"`
	StartedAt     string          `json:"started_at,omitempty"`
	DurationNS    int64           `json:"duration_ns,omitempty"`
	CgroupVersion string          `json:"cgroup_version,omitempty"`
	CacheSource   string          `json:"cache_source,omitempty"`
	Filesystem    string          `json:"filesystem,omitempty"`
	Rotations     int             `json:"rotations,omitempty"`
	Workload      WorkloadConfig  `json:"workload"`
	PeakMemory    uint64          `json:"peak_memory,omitempty"`
	PeakRSS       uint64          `json:"peak_rss,omitempty"`
	Cache         CacheMetrics    `json:"cache"`
	Writer        WriterState     `json:"writer"`
	Integrity     IntegrityReport `json:"integrity"`
	Error         string          `json:"error,omitempty"`
}

type ComparisonReport struct {
	SchemaVersion int          `json:"schema_version"`
	Runs          []RunSummary `json:"runs"`
}

type SweepAttempt struct {
	Strategy         string      `json:"strategy"`
	LimitMiB         int         `json:"limit_mib"`
	Repetition       int         `json:"repetition"`
	ExitCode         int         `json:"exit_code"`
	OOM              bool        `json:"oom"`
	TimedOut         bool        `json:"timed_out"`
	FunctionalPassed bool        `json:"functional_passed"`
	IntegrityPassed  bool        `json:"integrity_passed"`
	Passed           bool        `json:"passed"`
	FailureKind      string      `json:"failure_kind,omitempty"`
	Error            string      `json:"error,omitempty"`
	Summary          *RunSummary `json:"summary,omitempty"`
}

type SweepReport struct {
	SchemaVersion      int            `json:"schema_version"`
	Strategy           string         `json:"strategy"`
	MinimumPassMiB     int            `json:"minimum_pass_mib"`
	GreatestFailMiB    int            `json:"greatest_fail_mib"`
	OOMFailures        int            `json:"oom_failures"`
	TimeoutFailures    int            `json:"timeout_failures"`
	FunctionalFailures int            `json:"functional_failures"`
	IntegrityFailures  int            `json:"integrity_failures"`
	LowerMiB           int            `json:"lower_mib"`
	UpperMiB           int            `json:"upper_mib"`
	StepMiB            int            `json:"step_mib"`
	Repetitions        int            `json:"repetitions"`
	AttemptTimeoutNS   int64          `json:"attempt_timeout_ns,omitempty"`
	Workload           WorkloadConfig `json:"workload"`
	Attempts           []SweepAttempt `json:"attempts"`
}

func WriteJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename report: %w", err)
	}
	ok = true
	return nil
}

func NowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }
