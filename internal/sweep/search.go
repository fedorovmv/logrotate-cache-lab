package sweep

import (
	"context"
	"fmt"

	"logrotate-cache-lab/internal/report"
)

type AttemptRunner func(context.Context, report.SweepAttempt) report.SweepAttempt

type Config struct {
	LowerMiB, UpperMiB, StepMiB, Repetitions int
	Strategy                                 string
}

func Search(ctx context.Context, cfg Config, run AttemptRunner) (report.SweepReport, error) {
	result := report.SweepReport{
		SchemaVersion: report.SchemaVersion, Strategy: cfg.Strategy,
		LowerMiB: cfg.LowerMiB, UpperMiB: cfg.UpperMiB, StepMiB: cfg.StepMiB, Repetitions: cfg.Repetitions,
	}
	if cfg.LowerMiB <= 0 || cfg.UpperMiB < cfg.LowerMiB || cfg.StepMiB <= 0 || cfg.Repetitions <= 0 {
		return result, fmt.Errorf("invalid sweep configuration")
	}
	lower := alignUp(cfg.LowerMiB, cfg.StepMiB)
	upper := alignDown(cfg.UpperMiB, cfg.StepMiB)
	if lower > upper {
		return result, fmt.Errorf("empty aligned range")
	}
	tested := map[int]bool{}
	test := func(limit int) (bool, error) {
		if passed, ok := tested[limit]; ok {
			return passed, nil
		}
		passed := true
		for repetition := 1; repetition <= cfg.Repetitions; repetition++ {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			default:
			}
			attempt := run(ctx, report.SweepAttempt{Strategy: cfg.Strategy, LimitMiB: limit, Repetition: repetition})
			result.Attempts = append(result.Attempts, attempt)
			switch attempt.FailureKind {
			case "oom":
				result.OOMFailures++
			case "functional":
				result.FunctionalFailures++
			case "integrity":
				result.IntegrityFailures++
			case "timeout":
				result.TimeoutFailures++
			}
			passed = passed && attempt.Passed
		}
		tested[limit] = passed
		return passed, nil
	}
	upperPass, err := test(upper)
	if err != nil {
		return result, err
	}
	if !upperPass {
		result.GreatestFailMiB = upper
		return result, fmt.Errorf("upper memory limit %d MiB did not pass", upper)
	}
	lowerPass, err := test(lower)
	if err != nil {
		return result, err
	}
	if lowerPass {
		result.MinimumPassMiB = lower
		return result, nil
	}
	fail, pass := lower, upper
	for pass-fail > cfg.StepMiB {
		mid := alignDown((fail+pass)/2, cfg.StepMiB)
		if mid <= fail {
			mid = fail + cfg.StepMiB
		}
		midPass, err := test(mid)
		if err != nil {
			return result, err
		}
		if midPass {
			pass = mid
		} else {
			fail = mid
		}
	}
	result.MinimumPassMiB = pass
	result.GreatestFailMiB = fail
	result.BoundaryResolved = true
	return result, nil
}

func alignUp(value, step int) int {
	return ((value + step - 1) / step) * step
}

func alignDown(value, step int) int {
	return (value / step) * step
}
