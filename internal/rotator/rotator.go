package rotator

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"logrotate-cache-lab/internal/report"
)

type Strategy string

const (
	CopyTruncate Strategy = "copytruncate"
	RenameReopen Strategy = "rename-reopen"
)

type Event = report.RotationEvent

type Config struct {
	Strategy     Strategy
	ActivePath   string
	ReopenURL    string
	EventPath    string
	MaxFileBytes int64
	Rotations    int
	MaxBackups   int
	PollInterval time.Duration
	StartTime    time.Time
}

func Run(ctx context.Context, cfg Config) error {
	if cfg.MaxFileBytes <= 0 || cfg.Rotations <= 0 {
		return fmt.Errorf("max file bytes and rotations must be positive")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 20 * time.Millisecond
	}
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	ordinal := 1
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			info, err := os.Stat(cfg.ActivePath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			if info.Size() < cfg.MaxFileBytes {
				continue
			}
			if err := RotateOnce(ctx, cfg, ordinal); err != nil {
				_ = appendEvent(cfg, Event{Ordinal: ordinal, Phase: "rotation-failed", Bytes: info.Size(), Error: err.Error()})
				return err
			}
			ordinal++
			if ordinal > cfg.Rotations {
				return nil
			}
		}
	}
}

func RotateOnce(ctx context.Context, cfg Config, ordinal int) error {
	info, err := os.Stat(cfg.ActivePath)
	if err != nil {
		return fmt.Errorf("stat active log: %w", err)
	}
	if err := appendEvent(cfg, Event{Ordinal: ordinal, Phase: "threshold", Bytes: info.Size()}); err != nil {
		return err
	}
	switch cfg.Strategy {
	case CopyTruncate:
		err = rotateCopyTruncate(cfg, ordinal)
	case RenameReopen:
		err = rotateRenameReopen(ctx, cfg, ordinal, info)
	default:
		err = fmt.Errorf("unknown strategy %q", cfg.Strategy)
	}
	if err != nil {
		return fmt.Errorf("%s rotation %d: %w", cfg.Strategy, ordinal, err)
	}
	if err := EnforceRetention(cfg.ActivePath, cfg.MaxBackups); err != nil {
		return fmt.Errorf("retention: %w", err)
	}
	return appendEvent(cfg, Event{Ordinal: ordinal, Phase: "retention-complete", Bytes: info.Size()})
}

func BackupPath(active string, ordinal int) string {
	return fmt.Sprintf("%s.%06d", active, ordinal)
}

func EnforceRetention(active string, maxBackups int) error {
	if maxBackups <= 0 {
		return nil
	}
	matches, err := filepath.Glob(active + ".*")
	if err != nil {
		return err
	}
	type backup struct {
		path    string
		ordinal int
	}
	backups := make([]backup, 0, len(matches))
	for _, match := range matches {
		suffix := strings.TrimPrefix(match, active+".")
		ordinal, err := strconv.Atoi(suffix)
		if err == nil {
			backups = append(backups, backup{path: match, ordinal: ordinal})
		}
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].ordinal < backups[j].ordinal })
	for len(backups) > maxBackups {
		if err := os.Remove(backups[0].path); err != nil {
			return err
		}
		backups = backups[1:]
	}
	return nil
}

func appendEvent(cfg Config, event Event) error {
	if cfg.EventPath == "" {
		return nil
	}
	if cfg.StartTime.IsZero() {
		cfg.StartTime = time.Now()
	}
	now := time.Now()
	event.ElapsedNS = now.Sub(cfg.StartTime).Nanoseconds()
	event.Timestamp = now.UTC().Format(time.RFC3339Nano)
	if err := os.MkdirAll(filepath.Dir(cfg.EventPath), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(cfg.EventPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	writer := csv.NewWriter(f)
	if info, statErr := f.Stat(); statErr == nil && info.Size() == 0 {
		if err := writer.Write([]string{"elapsed_ns", "timestamp", "ordinal", "phase", "bytes", "error"}); err != nil {
			return err
		}
	}
	if err := writer.Write([]string{
		strconv.FormatInt(event.ElapsedNS, 10), event.Timestamp, strconv.Itoa(event.Ordinal), event.Phase,
		strconv.FormatInt(event.Bytes, 10), event.Error,
	}); err != nil {
		return err
	}
	writer.Flush()
	return writer.Error()
}

func ReadEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	var events []Event
	for i, row := range rows {
		if i == 0 || len(row) < 6 {
			continue
		}
		elapsed, _ := strconv.ParseInt(row[0], 10, 64)
		ordinal, _ := strconv.Atoi(row[2])
		bytes, _ := strconv.ParseInt(row[4], 10, 64)
		events = append(events, Event{ElapsedNS: elapsed, Timestamp: row[1], Ordinal: ordinal, Phase: row[3], Bytes: bytes, Error: row[5]})
	}
	return events, nil
}
