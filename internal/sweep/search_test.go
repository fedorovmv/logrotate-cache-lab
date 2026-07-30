package sweep

import (
	"context"
	"testing"

	"logrotate-cache-lab/internal/report"
)

func TestSearchFindsAlignedBoundaryWithThreePasses(t *testing.T) {
	counts := map[int]int{}
	runner := func(_ context.Context, attempt report.SweepAttempt) report.SweepAttempt {
		counts[attempt.LimitMiB]++
		attempt.Passed = attempt.LimitMiB >= 92
		return attempt
	}
	got, err := Search(context.Background(), Config{LowerMiB: 64, UpperMiB: 128, StepMiB: 4, Repetitions: 3, Strategy: "rename-reopen"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if got.MinimumPassMiB != 92 || got.GreatestFailMiB != 88 {
		t.Fatalf("got=%+v", got)
	}
	for limit, count := range counts {
		if count != 3 {
			t.Fatalf("limit %d repetitions=%d", limit, count)
		}
		if limit%4 != 0 {
			t.Fatalf("unaligned limit %d", limit)
		}
	}
}

func TestSearchRejectsCandidateWhenOneRepetitionFails(t *testing.T) {
	runner := func(_ context.Context, attempt report.SweepAttempt) report.SweepAttempt {
		attempt.Passed = attempt.LimitMiB >= 100 && !(attempt.LimitMiB == 100 && attempt.Repetition == 2)
		return attempt
	}
	got, err := Search(context.Background(), Config{LowerMiB: 96, UpperMiB: 104, StepMiB: 4, Repetitions: 3, Strategy: "copytruncate"}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if got.MinimumPassMiB != 104 || got.GreatestFailMiB != 100 {
		t.Fatalf("got=%+v", got)
	}
}

func TestSearchReportsOOMAndFunctionalFailureCounts(t *testing.T) {
	runner := func(_ context.Context, attempt report.SweepAttempt) report.SweepAttempt {
		attempt.Passed = attempt.LimitMiB >= 96
		if !attempt.Passed {
			if attempt.LimitMiB == 64 {
				attempt.OOM = true
				attempt.FailureKind = "oom"
			} else {
				attempt.FailureKind = "functional"
			}
		}
		return attempt
	}

	got, err := Search(context.Background(), Config{LowerMiB: 64, UpperMiB: 128, StepMiB: 32, Repetitions: 1}, runner)
	if err != nil {
		t.Fatal(err)
	}
	if got.OOMFailures != 1 || got.FunctionalFailures != 0 {
		t.Fatalf("failure counts=%+v", got)
	}
}
