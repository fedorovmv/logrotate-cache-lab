package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"

	"logrotate-cache-lab/internal/cgroup"
	"logrotate-cache-lab/internal/integrity"
	"logrotate-cache-lab/internal/metrics"
	"logrotate-cache-lab/internal/monitor"
	"logrotate-cache-lab/internal/report"
	"logrotate-cache-lab/internal/rotator"
)

type Config struct {
	Executable       string
	WriterExecutable string
	RunID            string
	Strategy         rotator.Strategy
	LogDir           string
	ResultDir        string
	MaxFileBytes     int64
	Rotations        int
	MaxBackups       int
	RecordBytes      int
	BytesPerSecond   int64
	BufferBytes      int
	FlushInterval    time.Duration
	MonitorInterval  time.Duration
	ResidentBytes    int64
	EnableMonitor    bool
}

func Run(ctx context.Context, cfg Config) (report.RunSummary, error) {
	start := time.Now()
	if cfg.Executable == "" {
		cfg.Executable = os.Args[0]
	}
	if cfg.RunID == "" || cfg.LogDir == "" || cfg.ResultDir == "" || cfg.MaxFileBytes <= 0 || cfg.Rotations <= 0 {
		return report.RunSummary{}, fmt.Errorf("invalid harness configuration")
	}
	if err := os.MkdirAll(cfg.LogDir, 0o755); err != nil {
		return report.RunSummary{}, err
	}
	if err := os.MkdirAll(cfg.ResultDir, 0o755); err != nil {
		return report.RunSummary{}, err
	}
	active := filepath.Join(cfg.LogDir, "envoy.log")
	statePath := filepath.Join(cfg.ResultDir, "writer-state.json")
	eventPath := filepath.Join(cfg.ResultDir, "events.csv")
	samplesPath := filepath.Join(cfg.ResultDir, "samples.csv")
	listen, err := freeLoopbackAddress()
	if err != nil {
		return report.RunSummary{}, err
	}

	writerArgs := []string{"--run-id", cfg.RunID, "--path", active, "--listen", listen,
		"--state", statePath, "--record-bytes", strconv.Itoa(cfg.RecordBytes), "--buffer-bytes", strconv.Itoa(cfg.BufferBytes),
		"--bytes-per-second", strconv.FormatInt(cfg.BytesPerSecond, 10), "--flush-interval", cfg.FlushInterval.String(),
		"--resident-bytes", strconv.FormatInt(cfg.ResidentBytes, 10)}
	writerExecutable, writerArgs := writerInvocation(cfg.Executable, cfg.WriterExecutable, writerArgs)
	writerCmd, writerLog, err := startChild(writerExecutable, filepath.Join(cfg.ResultDir, "writer.log"), writerArgs...)
	if err != nil {
		return report.RunSummary{}, err
	}
	defer writerLog.Close()
	if err := waitHTTP(ctx, "http://"+listen+"/healthz", 5*time.Second); err != nil {
		_ = stopChild(writerCmd, 2*time.Second)
		return report.RunSummary{}, err
	}

	var monitorCmd *exec.Cmd
	var monitorLog *os.File
	if cfg.EnableMonitor {
		monitorArgs := []string{"monitor", "--run-id", cfg.RunID, "--strategy", string(cfg.Strategy), "--active", active,
			"--samples", samplesPath, "--interval", cfg.MonitorInterval.String(), "--start-unix-nano", strconv.FormatInt(start.UnixNano(), 10)}
		monitorCmd, monitorLog, err = startChild(cfg.Executable, filepath.Join(cfg.ResultDir, "monitor.log"), monitorArgs...)
		if err != nil {
			_ = stopChild(writerCmd, 2*time.Second)
			return report.RunSummary{}, err
		}
		defer monitorLog.Close()
	}

	rotateArgs := []string{"rotate", "--strategy", string(cfg.Strategy), "--active", active,
		"--reopen-url", "http://" + listen, "--events", eventPath, "--max-file-bytes", strconv.FormatInt(cfg.MaxFileBytes, 10),
		"--rotations", strconv.Itoa(cfg.Rotations), "--max-backups", strconv.Itoa(cfg.MaxBackups),
		"--start-unix-nano", strconv.FormatInt(start.UnixNano(), 10)}
	rotatorCmd, rotatorLog, err := startChild(cfg.Executable, filepath.Join(cfg.ResultDir, "rotator.log"), rotateArgs...)
	if err != nil {
		_ = stopChild(writerCmd, 2*time.Second)
		if monitorCmd != nil {
			_ = stopChild(monitorCmd, 2*time.Second)
		}
		return report.RunSummary{}, err
	}
	defer rotatorLog.Close()
	rotatorErr := waitCommand(ctx, rotatorCmd)
	if rotatorErr == nil && cfg.Strategy == rotator.RenameReopen {
		_ = waitReopens(ctx, "http://"+listen+"/state", uint64(cfg.Rotations), 2*time.Second)
	}
	writerErr := stopChild(writerCmd, 3*time.Second)
	var monitorErr error
	if monitorCmd != nil {
		monitorErr = stopChild(monitorCmd, 3*time.Second)
	}
	if rotatorErr != nil {
		return report.RunSummary{}, fmt.Errorf("rotator: %w", rotatorErr)
	}
	if writerErr != nil {
		return report.RunSummary{}, fmt.Errorf("writer: %w", writerErr)
	}
	if monitorErr != nil {
		return report.RunSummary{}, fmt.Errorf("monitor: %w", monitorErr)
	}

	var writerState report.WriterState
	if b, err := os.ReadFile(statePath); err != nil {
		return report.RunSummary{}, err
	} else if err := json.Unmarshal(b, &writerState); err != nil {
		return report.RunSummary{}, err
	}
	paths, err := filepath.Glob(active + ".*")
	if err != nil {
		return report.RunSummary{}, err
	}
	paths = append(paths, active)
	sort.Strings(paths)
	integrityReport, err := integrity.Analyze(cfg.RunID, writerState.Generated, paths)
	if err != nil {
		return report.RunSummary{}, err
	}
	if err := report.WriteJSONAtomic(filepath.Join(cfg.ResultDir, "integrity.json"), integrityReport); err != nil {
		return report.RunSummary{}, err
	}
	events, err := rotator.ReadEvents(eventPath)
	if err != nil {
		return report.RunSummary{}, err
	}
	var samples []report.Sample
	if cfg.EnableMonitor {
		samples, err = monitor.ReadSamples(samplesPath)
		if err != nil {
			return report.RunSummary{}, err
		}
	}
	summary := report.RunSummary{
		SchemaVersion: report.SchemaVersion, RunID: cfg.RunID, Strategy: string(cfg.Strategy), StartedAt: start.UTC().Format(time.RFC3339Nano),
		DurationNS: time.Since(start).Nanoseconds(), Rotations: cfg.Rotations, Writer: writerState,
		Integrity: integrityReport, Cache: metrics.Analyze(samples, events),
		Workload: report.WorkloadConfig{
			MaxFileBytes: cfg.MaxFileBytes, Rotations: cfg.Rotations, MaxBackups: cfg.MaxBackups,
			RecordBytes: cfg.RecordBytes, BytesPerSecond: cfg.BytesPerSecond, BufferBytes: cfg.BufferBytes,
			FlushIntervalNS: cfg.FlushInterval.Nanoseconds(), MonitorIntervalNS: cfg.MonitorInterval.Nanoseconds(),
			ResidentBytes: cfg.ResidentBytes,
		},
	}
	if cfg.WriterExecutable == "" {
		summary.WriterImplementation = "go"
	} else {
		summary.WriterImplementation = filepath.Base(cfg.WriterExecutable)
	}
	updatePeaks(&summary, samples)
	if reader, discoverErr := cgroup.Discover(); discoverErr == nil {
		if memory, sampleErr := reader.Sample(); sampleErr == nil {
			summary.CgroupVersion = string(memory.Version)
			summary.CacheSource = memory.CacheSource
		}
	}
	summary.Success = writerState.ReopenFailures == 0
	if cfg.Strategy == rotator.RenameReopen || cfg.Strategy == rotator.Baseline {
		summary.Success = summary.Success && integrityReport.Missing == 0 && integrityReport.Duplicates == 0 && integrityReport.Malformed == 0
	}
	if !summary.Success {
		summary.Error = "acceptance criteria failed"
	}
	if err := report.WriteJSONAtomic(filepath.Join(cfg.ResultDir, "summary.json"), summary); err != nil {
		return summary, err
	}
	return summary, nil
}

func writerInvocation(loglabExecutable, writerExecutable string, args []string) (string, []string) {
	if writerExecutable != "" {
		return writerExecutable, args
	}
	return loglabExecutable, append([]string{"writer"}, args...)
}

func updatePeaks(summary *report.RunSummary, samples []report.Sample) {
	for _, sample := range samples {
		if sample.MemoryCurrent > summary.PeakMemory {
			summary.PeakMemory = sample.MemoryCurrent
		}
		if sample.RSS > summary.PeakRSS {
			summary.PeakRSS = sample.RSS
		}
		if sample.Anon != nil && *sample.Anon > summary.PeakAnon {
			summary.PeakAnon = *sample.Anon
		}
	}
}

func freeLoopbackAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	address := listener.Addr().String()
	return address, listener.Close()
}

func startChild(executable, logPath string, args ...string) (*exec.Cmd, *os.File, error) {
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(executable, args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return nil, nil, err
	}
	return cmd, logFile, nil
}

func waitHTTP(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return fmt.Errorf("timeout waiting for %s", url)
}

func waitReopens(ctx context.Context, url string, want uint64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			var state report.WriterState
			decodeErr := json.NewDecoder(response.Body).Decode(&state)
			_ = response.Body.Close()
			if decodeErr == nil && state.ReopenRequests >= want {
				return nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %d reopen requests", want)
}

func waitCommand(ctx context.Context, cmd *exec.Cmd) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return ctx.Err()
	}
}

func stopChild(cmd *exec.Cmd, timeout time.Duration) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			return nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 0 {
			return nil
		}
		return err
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return fmt.Errorf("child stop timeout")
	}
}
