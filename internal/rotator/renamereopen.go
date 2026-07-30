package rotator

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"syscall"
	"time"
)

func rotateRenameReopen(ctx context.Context, cfg Config, ordinal int, info os.FileInfo) error {
	backup := BackupPath(cfg.ActivePath, ordinal)
	if err := os.Rename(cfg.ActivePath, backup); err != nil {
		return fmt.Errorf("rename active: %w", err)
	}
	if err := appendEvent(cfg, Event{Ordinal: ordinal, Phase: "rename", Bytes: info.Size()}); err != nil {
		return err
	}
	f, err := os.OpenFile(cfg.ActivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create replacement: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close replacement: %w", err)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if err := os.Chown(cfg.ActivePath, int(stat.Uid), int(stat.Gid)); err != nil {
			return fmt.Errorf("chown replacement: %w", err)
		}
	}
	if err := os.Chmod(cfg.ActivePath, info.Mode().Perm()); err != nil {
		return fmt.Errorf("chmod replacement: %w", err)
	}
	if err := appendEvent(cfg, Event{Ordinal: ordinal, Phase: "replacement-created"}); err != nil {
		return err
	}
	if cfg.ReopenURL == "" {
		return fmt.Errorf("reopen URL is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.ReopenURL+"/reopen-logs", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request reopen: %w", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("reopen status %d", response.StatusCode)
	}
	return appendEvent(cfg, Event{Ordinal: ordinal, Phase: "reopen-requested"})
}
