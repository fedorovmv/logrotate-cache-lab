package harness

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"logrotate-cache-lab/internal/rotator"
)

func TestRunRenameSubprocessesPreservesIntegrity(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "loglab")
	build := exec.Command("go", "build", "-o", executable, "./cmd/loglab")
	build.Dir = filepath.Join("..", "..")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, output)
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	summary, err := Run(ctx, Config{
		Executable: executable,
		RunID:      "test-run", Strategy: rotator.RenameReopen,
		LogDir: filepath.Join(root, "logs"), ResultDir: filepath.Join(root, "results"),
		MaxFileBytes: 64 * 1024, Rotations: 2, RecordBytes: 256,
		BytesPerSecond: 2 * 1024 * 1024, BufferBytes: 64 * 1024,
		FlushInterval: 20 * time.Millisecond, MonitorInterval: 20 * time.Millisecond,
		EnableMonitor: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Success || summary.Rotations != 2 {
		t.Fatalf("summary=%+v", summary)
	}
	if summary.Integrity.Missing != 0 || summary.Integrity.Duplicates != 0 || summary.Integrity.Malformed != 0 {
		t.Fatalf("integrity=%+v", summary.Integrity)
	}
}
