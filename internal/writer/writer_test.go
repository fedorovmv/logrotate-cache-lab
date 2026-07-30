package writer

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"logrotate-cache-lab/internal/record"
)

func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

func TestReopenSwitchesInodeWithoutLoss(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "active.log")
	backup := filepath.Join(dir, "backup.log")
	w, err := New(Config{
		RunID:          "run-a",
		Path:           active,
		ListenAddress:  "127.0.0.1:0",
		RecordBytes:    128,
		BufferBytes:    256,
		BytesPerSecond: 128 * 200,
		FlushInterval:  10 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	w.WaitReady(t.Context())
	waitFor(t, 2*time.Second, func() bool { return fileSize(active) >= 1024 })

	original, err := os.Stat(active)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(active, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(active, nil, original.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
	response, err := http.Post("http://"+w.Address()+"/reopen-logs", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	waitFor(t, 2*time.Second, func() bool { return fileSize(active) >= 1024 })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	renamed, _ := os.Stat(backup)
	replacement, _ := os.Stat(active)
	if !os.SameFile(original, renamed) {
		t.Fatal("backup is not the original inode")
	}
	if os.SameFile(original, replacement) {
		t.Fatal("replacement reused original inode")
	}

	seen := map[uint64]bool{}
	for _, path := range []string{backup, active} {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			r, err := record.Parse(append(scanner.Bytes(), '\n'))
			if err != nil {
				t.Fatal(err)
			}
			if seen[r.Sequence] {
				t.Fatalf("duplicate sequence %d", r.Sequence)
			}
			seen[r.Sequence] = true
		}
		_ = f.Close()
	}
	state := w.State()
	if state.ReopenFailures != 0 || state.Written != state.Generated {
		t.Fatalf("state=%+v", state)
	}
	for sequence := uint64(1); sequence <= state.Generated; sequence++ {
		if !seen[sequence] {
			t.Fatalf("missing sequence %d", sequence)
		}
	}
}
