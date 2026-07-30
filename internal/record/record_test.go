package record

import "testing"

func TestEncodeParseFixedRecord(t *testing.T) {
	got, err := Encode("run-a", 42, 128)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 128 {
		t.Fatalf("length=%d", len(got))
	}
	parsed, err := Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.RunID != "run-a" || parsed.Sequence != 42 {
		t.Fatalf("parsed=%+v", parsed)
	}
}

func TestEncodeRejectsRecordTooSmall(t *testing.T) {
	if _, err := Encode("run-a", 1, 8); err == nil {
		t.Fatal("expected size error")
	}
}

func TestParseRejectsMalformedRecord(t *testing.T) {
	if _, err := Parse([]byte("broken\n")); err == nil {
		t.Fatal("expected parse error")
	}
}
