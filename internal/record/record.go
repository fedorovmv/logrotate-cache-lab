package record

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

type Record struct {
	RunID    string
	Sequence uint64
}

func Encode(runID string, sequence uint64, size int) ([]byte, error) {
	if runID == "" || strings.ContainsAny(runID, " \t\r\n") {
		return nil, fmt.Errorf("run ID must be non-empty and contain no whitespace")
	}
	prefix := fmt.Sprintf("%s %020d ", runID, sequence)
	if size < len(prefix)+1 {
		return nil, fmt.Errorf("record size %d is smaller than minimum %d", size, len(prefix)+1)
	}
	b := make([]byte, size)
	copy(b, prefix)
	for i := len(prefix); i < len(b)-1; i++ {
		b[i] = 'x'
	}
	b[len(b)-1] = '\n'
	return b, nil
}

func Parse(line []byte) (Record, error) {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	parts := bytes.SplitN(line, []byte{' '}, 3)
	if len(parts) != 3 || len(parts[0]) == 0 || len(parts[1]) != 20 {
		return Record{}, fmt.Errorf("malformed record")
	}
	sequence, err := strconv.ParseUint(string(parts[1]), 10, 64)
	if err != nil {
		return Record{}, fmt.Errorf("parse sequence: %w", err)
	}
	return Record{RunID: string(parts[0]), Sequence: sequence}, nil
}
