package monitor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogFileStats(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "active.log")
	if err := os.WriteFile(active, make([]byte, 10), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(active+".000001", make([]byte, 20), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(active+".000002", make([]byte, 30), 0o640); err != nil {
		t.Fatal(err)
	}
	got, err := LogFileStats(active)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActiveBytes != 10 || got.BackupBytes != 50 || got.BackupCount != 2 {
		t.Fatalf("got=%+v", got)
	}
}
