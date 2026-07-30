package rotator

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
)

func phases(events []Event) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Phase)
	}
	return out
}

func TestCopyTruncateKeepsActiveInode(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "active.log")
	events := filepath.Join(dir, "events.csv")
	data := []byte("one\ntwo\n")
	if err := os.WriteFile(active, data, 0o640); err != nil {
		t.Fatal(err)
	}
	original, _ := os.Stat(active)
	cfg := Config{Strategy: CopyTruncate, ActivePath: active, EventPath: events, MaxBackups: 10}
	if err := RotateOnce(context.Background(), cfg, 1); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(active)
	if !os.SameFile(original, after) || after.Size() != 0 {
		t.Fatalf("active file not truncated in place")
	}
	got, err := os.ReadFile(BackupPath(active, 1))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("backup=%q", got)
	}
	logged, err := ReadEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"threshold", "copy-start", "copy-synced", "truncate", "retention-complete"}
	if !reflect.DeepEqual(phases(logged), want) {
		t.Fatalf("phases=%v want=%v", phases(logged), want)
	}
}

func TestRenameReopenSwitchesPath(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "active.log")
	events := filepath.Join(dir, "events.csv")
	if err := os.WriteFile(active, []byte("one\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	original, _ := os.Stat(active)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := Config{Strategy: RenameReopen, ActivePath: active, EventPath: events, ReopenURL: server.URL, MaxBackups: 10}
	if err := RotateOnce(context.Background(), cfg, 1); err != nil {
		t.Fatal(err)
	}
	backup, _ := os.Stat(BackupPath(active, 1))
	replacement, _ := os.Stat(active)
	if !os.SameFile(original, backup) || os.SameFile(original, replacement) {
		t.Fatal("unexpected inode relationship")
	}
	if replacement.Mode().Perm() != original.Mode().Perm() {
		t.Fatalf("mode=%v want=%v", replacement.Mode().Perm(), original.Mode().Perm())
	}
	if calls.Load() != 1 {
		t.Fatalf("reopen calls=%d", calls.Load())
	}
	logged, err := ReadEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"threshold", "rename", "replacement-created", "reopen-requested", "retention-complete"}
	if !reflect.DeepEqual(phases(logged), want) {
		t.Fatalf("phases=%v want=%v", phases(logged), want)
	}
}

func TestRetentionKeepsNewestBackups(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "active.log")
	for i := 1; i <= 4; i++ {
		if err := os.WriteFile(BackupPath(active, i), []byte{byte(i)}, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if err := EnforceRetention(active, 2); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 4; i++ {
		_, err := os.Stat(BackupPath(active, i))
		wantExists := i >= 3
		if wantExists != (err == nil) {
			t.Fatalf("backup %d existence=%v err=%v", i, err == nil, err)
		}
	}
}

func TestBaselineWaitsForLogicalBytesWithoutChangingFile(t *testing.T) {
	dir := t.TempDir()
	active := filepath.Join(dir, "active.log")
	events := filepath.Join(dir, "events.csv")
	if err := os.WriteFile(active, make([]byte, 128), 0o640); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(active)
	cfg := Config{Strategy: Baseline, ActivePath: active, EventPath: events, MaxFileBytes: 64, Rotations: 2}
	if err := Run(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(active)
	if !os.SameFile(before, after) || after.Size() != 128 {
		t.Fatalf("baseline changed active file")
	}
	logged, err := ReadEvents(events)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"threshold", "baseline-complete"}; !reflect.DeepEqual(phases(logged), want) {
		t.Fatalf("phases=%v want=%v", phases(logged), want)
	}
}
