package cppwriter

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"logrotate-cache-lab/internal/integrity"
	"logrotate-cache-lab/internal/report"
)

func TestCppWriterReopenPreservesSequenceIntegrity(t *testing.T) {
	compiler, err := exec.LookPath("c++")
	if err != nil {
		compiler, err = exec.LookPath("clang++")
	}
	if err != nil {
		t.Skip("C++ compiler not installed")
	}
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "logwriter-cpp")
	compile := exec.Command(compiler, "-std=c++17", "-O2", "-pthread", filepath.Join(repo, "cpp", "writer", "main.cc"), "-o", binary)
	if output, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("compile C++ writer: %v\n%s", err, output)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	dir := t.TempDir()
	active := filepath.Join(dir, "active.log")
	backup := filepath.Join(dir, "backup.log")
	statePath := filepath.Join(dir, "state.json")
	cmd := exec.Command(binary,
		"--run-id", "cpp-run", "--path", active, "--listen", address, "--state", statePath,
		"--record-bytes", "128", "--buffer-bytes", "256", "--bytes-per-second", strconv.Itoa(128*300),
		"--flush-interval", "10ms", "--resident-bytes", "0")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})
	waitFor(t, 3*time.Second, func() bool {
		response, err := http.Get("http://" + address + "/healthz")
		if err != nil {
			return false
		}
		_ = response.Body.Close()
		return response.StatusCode == http.StatusOK
	})
	waitFor(t, 3*time.Second, func() bool { return fileSize(active) >= 1024 })
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
	response, err := http.Post("http://"+address+"/reopen-logs", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reopen status=%d", response.StatusCode)
	}
	waitFor(t, 3*time.Second, func() bool { return fileSize(active) >= 1024 })
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state report.WriterState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if state.Generated == 0 || state.Generated != state.Written || state.ReopenRequests != 1 || state.ReopenFailures != 0 {
		t.Fatalf("state=%+v", state)
	}
	check, err := integrity.Analyze("cpp-run", state.Generated, []string{backup, active})
	if err != nil {
		t.Fatal(err)
	}
	if check.Missing != 0 || check.Duplicates != 0 || check.Malformed != 0 {
		t.Fatalf("integrity=%+v", check)
	}
}

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
