package monitor

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"logrotate-cache-lab/internal/cgroup"
	"logrotate-cache-lab/internal/report"
)

type FileStats struct {
	ActiveBytes uint64
	BackupBytes uint64
	BackupCount int
}

type Config struct {
	RunID, Strategy, ActivePath, SamplesPath string
	Interval                                 time.Duration
	StartTime                                time.Time
}

func LogFileStats(active string) (FileStats, error) {
	var result FileStats
	if info, err := os.Stat(active); err == nil {
		result.ActiveBytes = uint64(info.Size())
	} else if !os.IsNotExist(err) {
		return result, err
	}
	matches, err := filepath.Glob(active + ".*")
	if err != nil {
		return result, err
	}
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && info.Mode().IsRegular() {
			result.BackupBytes += uint64(info.Size())
			result.BackupCount++
		}
	}
	return result, nil
}

func Run(ctx context.Context, cfg Config) error {
	reader, err := cgroup.Discover()
	if err != nil {
		return err
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 100 * time.Millisecond
	}
	if cfg.StartTime.IsZero() {
		cfg.StartTime = time.Now()
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SamplesPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(cfg.SamplesPath)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	header := []string{"elapsed_ns", "timestamp", "strategy", "run_id", "memory_current", "cache", "rss", "anon", "inactive_file", "active_file", "file_dirty", "file_writeback", "shmem", "active_bytes", "backup_bytes", "backup_count"}
	if err := w.Write(header); err != nil {
		return err
	}
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.Flush()
			return w.Error()
		case now := <-ticker.C:
			memory, err := reader.Sample()
			if err != nil {
				return err
			}
			rss, _ := reader.RSSFromCgroupProcs()
			files, err := LogFileStats(cfg.ActivePath)
			if err != nil {
				return err
			}
			sample := report.Sample{
				ElapsedNS: now.Sub(cfg.StartTime).Nanoseconds(), Timestamp: now.UTC().Format(time.RFC3339Nano),
				Strategy: cfg.Strategy, RunID: cfg.RunID, MemoryCurrent: memory.Current, Cache: memory.Cache, RSS: rss,
				Anon: memory.Anon, InactiveFile: memory.InactiveFile, ActiveFile: memory.ActiveFile, Dirty: memory.Dirty,
				Writeback: memory.Writeback, Shmem: memory.Shmem, ActiveBytes: files.ActiveBytes,
				BackupBytes: files.BackupBytes, BackupCount: files.BackupCount,
			}
			if err := w.Write(sampleRow(sample)); err != nil {
				return err
			}
			w.Flush()
			if err := w.Error(); err != nil {
				return err
			}
		}
	}
}

func sampleRow(s report.Sample) []string {
	return []string{
		strconv.FormatInt(s.ElapsedNS, 10), s.Timestamp, s.Strategy, s.RunID,
		strconv.FormatUint(s.MemoryCurrent, 10), strconv.FormatUint(s.Cache, 10), strconv.FormatUint(s.RSS, 10),
		ptrString(s.Anon), ptrString(s.InactiveFile), ptrString(s.ActiveFile), ptrString(s.Dirty), ptrString(s.Writeback), ptrString(s.Shmem),
		strconv.FormatUint(s.ActiveBytes, 10), strconv.FormatUint(s.BackupBytes, 10), strconv.Itoa(s.BackupCount),
	}
}

func ReadSamples(path string) ([]report.Sample, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return nil, err
	}
	var samples []report.Sample
	for i, row := range rows {
		if i == 0 {
			continue
		}
		if len(row) != 16 {
			return nil, fmt.Errorf("sample row %d has %d columns", i+1, len(row))
		}
		s := report.Sample{Timestamp: row[1], Strategy: row[2], RunID: row[3]}
		s.ElapsedNS, _ = strconv.ParseInt(row[0], 10, 64)
		s.MemoryCurrent, _ = strconv.ParseUint(row[4], 10, 64)
		s.Cache, _ = strconv.ParseUint(row[5], 10, 64)
		s.RSS, _ = strconv.ParseUint(row[6], 10, 64)
		s.Anon = parsePtr(row[7])
		s.InactiveFile = parsePtr(row[8])
		s.ActiveFile = parsePtr(row[9])
		s.Dirty = parsePtr(row[10])
		s.Writeback = parsePtr(row[11])
		s.Shmem = parsePtr(row[12])
		s.ActiveBytes, _ = strconv.ParseUint(row[13], 10, 64)
		s.BackupBytes, _ = strconv.ParseUint(row[14], 10, 64)
		s.BackupCount, _ = strconv.Atoi(row[15])
		samples = append(samples, s)
	}
	return samples, nil
}

func ptrString(value *uint64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatUint(*value, 10)
}

func parsePtr(value string) *uint64 {
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return nil
	}
	return &parsed
}
