package rotator

import (
	"fmt"
	"io"
	"os"
)

func rotateCopyTruncate(cfg Config, ordinal int) error {
	if err := appendEvent(cfg, Event{Ordinal: ordinal, Phase: "copy-start"}); err != nil {
		return err
	}
	in, err := os.Open(cfg.ActivePath)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(BackupPath(cfg.ActivePath, ordinal), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = out.Close()
		}
	}()
	written, err := io.Copy(out, in)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if err := out.Sync(); err != nil {
		return fmt.Errorf("sync backup: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close backup: %w", err)
	}
	closed = true
	if err := appendEvent(cfg, Event{Ordinal: ordinal, Phase: "copy-synced", Bytes: written}); err != nil {
		return err
	}
	if err := os.Truncate(cfg.ActivePath, 0); err != nil {
		return fmt.Errorf("truncate active: %w", err)
	}
	return appendEvent(cfg, Event{Ordinal: ordinal, Phase: "truncate", Bytes: written})
}
