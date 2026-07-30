package sweep

import (
	"testing"
	"time"

	"logrotate-cache-lab/internal/report"
	"logrotate-cache-lab/internal/rotator"
)

func TestDockerWorkloadReportContainsSizingInputs(t *testing.T) {
	got := dockerWorkload(DockerConfig{
		MaxFileBytes: 32 << 20, BytesPerSecond: 8 << 20, ResidentBytes: 48 << 20,
		Rotations: 4, RecordBytes: 512, BufferBytes: 64 << 10,
		FlushInterval: 100 * time.Millisecond, MonitorInterval: 250 * time.Millisecond,
	})
	if got.MaxFileBytes != 32<<20 || got.ResidentBytes != 48<<20 || got.Rotations != 4 || got.MonitorIntervalNS != int64(250*time.Millisecond) {
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
