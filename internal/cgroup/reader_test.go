package cgroup

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFixture(t *testing.T, dir, name, data string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func ptrValue(t *testing.T, p *uint64) uint64 {
	t.Helper()
	if p == nil {
		t.Fatal("nil counter")
	}
	return *p
}

func TestReaderV2(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "memory.current", "104857600\n")
	writeFixture(t, dir, "memory.stat", "anon 33554432\nfile 67108864\ninactive_file 50331648\nactive_file 16777216\nfile_dirty 4194304\nfile_writeback 1048576\nshmem 0\n")

	got, err := NewReader(dir, VersionV2).Sample()
	if err != nil {
		t.Fatal(err)
	}
	if got.Current != 104857600 || got.Cache != 67108864 || got.CacheSource != "file" {
		t.Fatalf("got=%+v", got)
	}
	if ptrValue(t, got.Anon) != 33554432 || ptrValue(t, got.Dirty) != 4194304 || ptrValue(t, got.Shmem) != 0 {
		t.Fatalf("got=%+v", got)
	}
}

func TestReaderV1PrefersTotalCache(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "memory.usage_in_bytes", "73400320\n")
	writeFixture(t, dir, "memory.stat", "cache 100\ntotal_cache 200\ninactive_file 80\nactive_file 120\n")

	got, err := NewReader(dir, VersionV1).Sample()
	if err != nil {
		t.Fatal(err)
	}
	if got.Current != 73400320 || got.Cache != 200 || got.CacheSource != "total_cache" {
		t.Fatalf("got=%+v", got)
	}
}
