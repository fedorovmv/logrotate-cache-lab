package sweep

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"logrotate-cache-lab/internal/report"
	"logrotate-cache-lab/internal/rotator"
)

type DockerConfig struct {
	Image, ResultRoot                           string
	LowerMiB, UpperMiB, StepMiB, Repetitions    int
	MaxFileBytes, BytesPerSecond, ResidentBytes int64
	Rotations, RecordBytes, BufferBytes         int
	FlushInterval, MonitorInterval              time.Duration
	AttemptTimeout                              time.Duration
}

func DockerSearch(ctx context.Context, cfg DockerConfig, strategy rotator.Strategy) (report.SweepReport, error) {
	return Search(ctx, Config{
		LowerMiB: cfg.LowerMiB, UpperMiB: cfg.UpperMiB, StepMiB: cfg.StepMiB,
		Repetitions: cfg.Repetitions, Strategy: string(strategy),
	}, func(ctx context.Context, attempt report.SweepAttempt) report.SweepAttempt {
		return dockerAttempt(ctx, cfg, strategy, attempt)
	})
}

func dockerAttempt(ctx context.Context, cfg DockerConfig, strategy rotator.Strategy, attempt report.SweepAttempt) report.SweepAttempt {
	runDir := filepath.Join(cfg.ResultRoot, fmt.Sprintf("%s-%dm-r%d-%d", strategy, attempt.LimitMiB, attempt.Repetition, time.Now().UnixNano()))
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		attempt.Error = err.Error()
		return attempt
	}
	volume := fmt.Sprintf("loglab-sweep-%s-%d-%d-%d", strategy, attempt.LimitMiB, attempt.Repetition, time.Now().UnixNano())
	container := fmt.Sprintf("loglab-sweep-run-%s-%d-%d-%d", strategy, attempt.LimitMiB, attempt.Repetition, time.Now().UnixNano())
	if output, err := exec.CommandContext(ctx, "docker", "volume", "create", volume).CombinedOutput(); err != nil {
		attempt.Error = fmt.Sprintf("create volume: %v: %s", err, strings.TrimSpace(string(output)))
		return attempt
	}
	defer func() { _ = exec.Command("docker", "volume", "rm", "-f", volume).Run() }()
	defer func() { _ = exec.Command("docker", "rm", "-f", container).Run() }()
	args := []string{"run", "--name", container, "--memory=" + strconv.Itoa(attempt.LimitMiB) + "m", "--memory-swap=" + strconv.Itoa(attempt.LimitMiB) + "m",
		"--mount", "source=" + volume + ",target=/var/log/loglab", "--mount", "type=bind,source=" + runDir + ",target=/results", cfg.Image,
		"run", "--run-id", fmt.Sprintf("%s-%d-%d", strategy, attempt.LimitMiB, attempt.Repetition), "--strategy", string(strategy),
		"--log-dir", "/var/log/loglab", "--result-dir", "/results", "--max-file-bytes", strconv.FormatInt(cfg.MaxFileBytes, 10),
		"--rotations", strconv.Itoa(cfg.Rotations), "--max-backups", "0", "--record-bytes", strconv.Itoa(cfg.RecordBytes),
		"--bytes-per-second", strconv.FormatInt(cfg.BytesPerSecond, 10), "--buffer-bytes", strconv.Itoa(cfg.BufferBytes),
		"--flush-interval", cfg.FlushInterval.String(), "--monitor-interval", cfg.MonitorInterval.String(), "--resident-bytes", strconv.FormatInt(cfg.ResidentBytes, 10)}
	attemptTimeout := cfg.AttemptTimeout
	if attemptTimeout <= 0 {
		attemptTimeout = 90 * time.Second
	}
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	var output bytes.Buffer
	cmd := exec.CommandContext(attemptCtx, "docker", args...)
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return classifyProcessFailure(attempt, -1, false, false, err.Error())
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var err error
	oomKilled := false
waitLoop:
	for {
		select {
		case err = <-done:
			break waitLoop
		case <-ticker.C:
			if containerOOMKilled(container) {
				oomKilled = true
				_ = exec.Command("docker", "rm", "-f", container).Run()
				err = <-done
				break waitLoop
			}
		case <-attemptCtx.Done():
			_ = exec.Command("docker", "rm", "-f", container).Run()
			err = <-done
			break waitLoop
		}
	}
	if err != nil {
		exitCode := -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		return classifyProcessFailure(attempt, exitCode, errors.Is(attemptCtx.Err(), context.DeadlineExceeded), oomKilled, strings.TrimSpace(output.String()))
	}
	attempt.ExitCode = 0
	summaryPath := filepath.Join(runDir, "summary.json")
	b, readErr := os.ReadFile(summaryPath)
	if readErr != nil {
		attempt.Error = fmt.Sprintf("read summary: %v; output: %s", readErr, output.String())
		attempt.FailureKind = "functional"
		return attempt
	}
	var summary report.RunSummary
	if err := json.Unmarshal(b, &summary); err != nil {
		attempt.Error = fmt.Sprintf("parse summary: %v", err)
		attempt.FailureKind = "functional"
		return attempt
	}
	return classifySummary(strategy, attempt, summary)
}

func classifyProcessFailure(attempt report.SweepAttempt, exitCode int, timedOut, oomKilled bool, output string) report.SweepAttempt {
	attempt.ExitCode = exitCode
	attempt.TimedOut = timedOut
	attempt.OOM = oomKilled || exitCode == 137
	attempt.Error = output
	if attempt.Error == "" {
		attempt.Error = "docker attempt failed"
	}
	if attempt.OOM {
		attempt.FailureKind = "oom"
	} else if timedOut {
		attempt.FailureKind = "timeout"
	} else {
		attempt.FailureKind = "functional"
	}
	return attempt
}

func containerOOMKilled(name string) bool {
	output, err := exec.Command("docker", "inspect", "--format", "{{.State.OOMKilled}}", name).Output()
	return err == nil && strings.TrimSpace(string(output)) == "true"
}

func classifySummary(strategy rotator.Strategy, attempt report.SweepAttempt, summary report.RunSummary) report.SweepAttempt {
	attempt.Summary = &summary
	attempt.FunctionalPassed = summary.SchemaVersion == report.SchemaVersion &&
		summary.Integrity.SchemaVersion == report.SchemaVersion &&
		summary.Writer.ReopenFailures == 0
	attempt.IntegrityPassed = summary.Integrity.Missing == 0 &&
		summary.Integrity.Duplicates == 0 &&
		summary.Integrity.Malformed == 0 &&
		summary.Integrity.WrongRunID == 0 &&
		summary.Integrity.DescendingTransitions == 0
	attempt.Passed = attempt.FunctionalPassed && (strategy == rotator.CopyTruncate || attempt.IntegrityPassed)
	if !attempt.FunctionalPassed {
		attempt.FailureKind = "functional"
		attempt.Error = "invalid summary schema or writer reopen failure"
	} else if !attempt.Passed {
		attempt.FailureKind = "integrity"
		attempt.Error = "integrity acceptance failed"
	}
	return attempt
}
