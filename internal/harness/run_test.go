package harness

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"logrotate-cache-lab/internal/report"
	"logrotate-cache-lab/internal/rotator"
)

func TestWriterInvocationUsesCppOverrideWithoutGoSubcommand(t *testing.T) {
	executable, args := writerInvocation("/usr/local/bin/loglab", "/usr/local/bin/logwriter-cpp", []string{"--run-id", "x"})
	if executable != "/usr/local/bin/logwriter-cpp" || len(args) != 2 || args[0] != "--run-id" {
		t.Fatalf("executable=%q args=%v", executable, args)
	}
	executable, args = writerInvocation("/usr/local/bin/loglab", "", []string{"--run-id", "x"})
	if executable != "/usr/local/bin/loglab" || len(args) != 3 || args[0] != "writer" {
		t.Fatalf("fallback executable=%q args=%v", executable, args)
	}
}

func TestUpdatePeaksSeparatesAnonFromMemoryAndRSS(t *testing.T) {
	anon1, anon2 := uint64(10), uint64(30)
	summary := report.RunSummary{}
	updatePeaks(&summary, []report.Sample{
		{MemoryCurrent: 100, RSS: 40, Anon: &anon1},
		{MemoryCurrent: 80, RSS: 50, Anon: &anon2},
	})
	if summary.PeakMemory != 100 || summary.PeakRSS != 50 || summary.PeakAnon != 30 {
		t.Fatalf("summary=%+v", summary)
	}
}

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
	if summary.Workload.MaxFileBytes != 64*1024 || summary.Workload.Rotations != 2 || summary.Workload.RecordBytes != 256 {
		t.Fatalf("workload=%+v", summary.Workload)
	}
	if summary.WriterImplementation != "go" {
		t.Fatalf("writer implementation=%q", summary.WriterImplementation)
	}
}
