package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"logrotate-cache-lab/internal/harness"
	"logrotate-cache-lab/internal/monitor"
	"logrotate-cache-lab/internal/rotator"
	"logrotate-cache-lab/internal/writer"
)

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: loglab <writer|rotate|monitor|run> [flags]")
	}
	var err error
	switch os.Args[1] {
	case "writer":
		err = writerCommand(os.Args[2:])
	case "rotate":
		err = rotateCommand(os.Args[2:])
	case "monitor":
		err = monitorCommand(os.Args[2:])
	case "run":
		err = runCommand(os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func writerCommand(args []string) error {
	fs := flag.NewFlagSet("writer", flag.ContinueOnError)
	var cfg writer.Config
	fs.StringVar(&cfg.RunID, "run-id", "", "run identifier")
	fs.StringVar(&cfg.Path, "path", "", "active log path")
	fs.StringVar(&cfg.ListenAddress, "listen", "127.0.0.1:18080", "HTTP listen address")
	fs.StringVar(&cfg.StatePath, "state", "", "final writer state path")
	fs.IntVar(&cfg.RecordBytes, "record-bytes", 512, "fixed record size")
	fs.IntVar(&cfg.BufferBytes, "buffer-bytes", 64*1024, "flush buffer size")
	fs.Int64Var(&cfg.BytesPerSecond, "bytes-per-second", 8*1024*1024, "target log rate")
	fs.DurationVar(&cfg.FlushInterval, "flush-interval", 100*time.Millisecond, "flush interval")
	fs.Int64Var(&cfg.ResidentBytes, "resident-bytes", 32*1024*1024, "touched resident allocation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	w, err := writer.New(cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return w.Run(ctx)
}

func rotateCommand(args []string) error {
	fs := flag.NewFlagSet("rotate", flag.ContinueOnError)
	var cfg rotator.Config
	var strategy string
	var startUnixNano int64
	fs.StringVar(&strategy, "strategy", "", "copytruncate or rename-reopen")
	fs.StringVar(&cfg.ActivePath, "active", "", "active log path")
	fs.StringVar(&cfg.ReopenURL, "reopen-url", "", "writer base URL")
	fs.StringVar(&cfg.EventPath, "events", "", "rotation event CSV")
	fs.Int64Var(&cfg.MaxFileBytes, "max-file-bytes", 32*1024*1024, "rotation threshold")
	fs.IntVar(&cfg.Rotations, "rotations", 4, "number of rotations")
	fs.IntVar(&cfg.MaxBackups, "max-backups", 0, "retained backups, zero is unlimited")
	fs.DurationVar(&cfg.PollInterval, "poll-interval", 20*time.Millisecond, "file size poll interval")
	fs.Int64Var(&startUnixNano, "start-unix-nano", 0, "shared run start time")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg.Strategy = rotator.Strategy(strategy)
	if startUnixNano != 0 {
		cfg.StartTime = time.Unix(0, startUnixNano)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return rotator.Run(ctx, cfg)
}

func monitorCommand(args []string) error {
	fs := flag.NewFlagSet("monitor", flag.ContinueOnError)
	var cfg monitor.Config
	var startUnixNano int64
	fs.StringVar(&cfg.RunID, "run-id", "", "run identifier")
	fs.StringVar(&cfg.Strategy, "strategy", "", "rotation strategy")
	fs.StringVar(&cfg.ActivePath, "active", "", "active log path")
	fs.StringVar(&cfg.SamplesPath, "samples", "", "sample CSV")
	fs.DurationVar(&cfg.Interval, "interval", 100*time.Millisecond, "sampling interval")
	fs.Int64Var(&startUnixNano, "start-unix-nano", 0, "shared run start time")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if startUnixNano != 0 {
		cfg.StartTime = time.Unix(0, startUnixNano)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	return monitor.Run(ctx, cfg)
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var cfg harness.Config
	var strategy string
	fs.StringVar(&cfg.RunID, "run-id", "run-"+strconv.FormatInt(time.Now().UnixNano(), 10), "run identifier")
	fs.StringVar(&strategy, "strategy", "copytruncate", "rotation strategy")
	fs.StringVar(&cfg.LogDir, "log-dir", "/var/log/loglab", "disk-backed log directory")
	fs.StringVar(&cfg.ResultDir, "result-dir", "/results", "result directory")
	fs.Int64Var(&cfg.MaxFileBytes, "max-file-bytes", 32*1024*1024, "rotation threshold")
	fs.IntVar(&cfg.Rotations, "rotations", 4, "number of rotations")
	fs.IntVar(&cfg.MaxBackups, "max-backups", 0, "retained backups")
	fs.IntVar(&cfg.RecordBytes, "record-bytes", 512, "fixed record size")
	fs.Int64Var(&cfg.BytesPerSecond, "bytes-per-second", 8*1024*1024, "target log rate")
	fs.IntVar(&cfg.BufferBytes, "buffer-bytes", 64*1024, "writer buffer")
	fs.DurationVar(&cfg.FlushInterval, "flush-interval", 100*time.Millisecond, "writer flush interval")
	fs.DurationVar(&cfg.MonitorInterval, "monitor-interval", 100*time.Millisecond, "monitor interval")
	fs.Int64Var(&cfg.ResidentBytes, "resident-bytes", 32*1024*1024, "touched resident allocation")
	fs.BoolVar(&cfg.EnableMonitor, "monitor", true, "enable cgroup monitor")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg.Strategy = rotator.Strategy(strategy)
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	cfg.Executable = executable
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	summary, err := harness.Run(ctx, cfg)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(summary); err != nil {
		return err
	}
	if !summary.Success {
		return fmt.Errorf("run acceptance failed")
	}
	return nil
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
