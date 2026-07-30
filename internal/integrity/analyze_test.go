package integrity

import (
	"os"
	"path/filepath"
	"testing"

	"logrotate-cache-lab/internal/record"
)

func writeRecords(t *testing.T, path string, sequences ...uint64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, sequence := range sequences {
		encoded, err := record.Encode("run-a", sequence, 64)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(encoded); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAnalyzeDetectsIntegrityProblems(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.log")
	second := filepath.Join(dir, "second.log")
	writeRecords(t, first, 1, 2, 2, 4)
	writeRecords(t, second, 9, 8)
	f, err := os.OpenFile(second, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("malformed\n")
	_ = f.Close()

	got, err := Analyze("run-a", 9, []string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if got.ValidUnique != 5 || got.Missing != 4 || got.Duplicates != 1 || got.Malformed != 1 || got.DescendingTransitions != 1 {
		t.Fatalf("got=%+v", got)
	}
}
