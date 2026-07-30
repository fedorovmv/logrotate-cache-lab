package sweep

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"logrotate-cache-lab/internal/report"
	"logrotate-cache-lab/internal/rotator"
)

func TestPrepareResultDirIsWritableByNonRootContainer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempt")
	if err := prepareResultDir(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o777 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
}

func TestAbsoluteResultRootNormalizesRelativeDockerBindSource(t *testing.T) {
	got, err := absoluteResultRoot(filepath.Join("results", "attempts"))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("path is not absolute: %q", got)
	}
}

func TestDockerWorkloadReportContainsSizingInputs(t *testing.T) {
	got := dockerWorkload(DockerConfig{
		MaxFileBytes: 32 << 20, BytesPerSecond: 8 << 20, ResidentBytes: 48 << 20,
		Rotations: 4, MaxBackups: 5, RecordBytes: 512, BufferBytes: 64 << 10,
		FlushInterval: 100 * time.Millisecond, MonitorInterval: 250 * time.Millisecond,
	})
	if got.MaxFileBytes != 32<<20 || got.ResidentBytes != 48<<20 || got.Rotations != 4 || got.MaxBackups != 5 || got.MonitorIntervalNS != int64(250*time.Millisecond) {
		t.Fatalf("workload=%+v", got)
	}
}

func TestClassifySummarySeparatesFunctionalAndIntegrityAcceptance(t *testing.T) {
	summary := report.RunSummary{
		SchemaVersion: report.SchemaVersion,
		Success:       true,
		Integrity: report.IntegrityReport{
			SchemaVersion: report.SchemaVersion,
			Missing:       7,
		},
	}

	copyAttempt := classifySummary(rotator.CopyTruncate, report.SweepAttempt{}, summary)
	if !copyAttempt.FunctionalPassed || copyAttempt.IntegrityPassed || !copyAttempt.Passed {
		t.Fatalf("copytruncate classification=%+v", copyAttempt)
	}

	renameAttempt := classifySummary(rotator.RenameReopen, report.SweepAttempt{}, summary)
	if !renameAttempt.FunctionalPassed || renameAttempt.IntegrityPassed || renameAttempt.Passed {
		t.Fatalf("rename-reopen classification=%+v", renameAttempt)
	}

	baselineAttempt := classifySummary(rotator.Baseline, report.SweepAttempt{}, summary)
	if baselineAttempt.Passed || baselineAttempt.FailureKind != "integrity" {
		t.Fatalf("baseline classification=%+v", baselineAttempt)
	}
}

func TestClassifySummaryRejectsReopenFailure(t *testing.T) {
	summary := report.RunSummary{
		SchemaVersion: report.SchemaVersion,
		Success:       true,
		Writer:        report.WriterState{ReopenFailures: 1},
		Integrity:     report.IntegrityReport{SchemaVersion: report.SchemaVersion},
	}

	attempt := classifySummary(rotator.RenameReopen, report.SweepAttempt{}, summary)
	if attempt.FunctionalPassed || attempt.Passed || attempt.FailureKind != "functional" {
		t.Fatalf("classification=%+v", attempt)
	}
}

func TestClassifyProcessFailureTreatsChildOOMOnTimeoutAsOOM(t *testing.T) {
	attempt := classifyProcessFailure(report.SweepAttempt{}, -1, true, true, "attempt timed out")
	if !attempt.OOM || !attempt.TimedOut || attempt.FailureKind != "oom" || attempt.Passed {
		t.Fatalf("classification=%+v", attempt)
	}
}

func TestClassifyProcessFailureSeparatesNonOOMTimeout(t *testing.T) {
	attempt := classifyProcessFailure(report.SweepAttempt{}, -1, true, false, "attempt timed out")
	if attempt.OOM || !attempt.TimedOut || attempt.FailureKind != "timeout" || attempt.Passed {
		t.Fatalf("classification=%+v", attempt)
	}
}
