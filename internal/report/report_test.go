package report

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWriteJSONAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	want := RunSummary{SchemaVersion: SchemaVersion, RunID: "run-a", Strategy: "copytruncate"}
	if err := WriteJSONAtomic(path, want); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got RunSummary
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got=%+v want=%+v", got, want)
	}
	if matches, _ := filepath.Glob(path + ".tmp-*"); len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}
