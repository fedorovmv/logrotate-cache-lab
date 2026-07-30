package integrity

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"

	"logrotate-cache-lab/internal/record"
	"logrotate-cache-lab/internal/report"
)

func Analyze(runID string, expected uint64, paths []string) (report.IntegrityReport, error) {
	result := report.IntegrityReport{
		SchemaVersion: report.SchemaVersion,
		Expected:      expected,
		Files:         make(map[string]report.FileSet),
	}
	seen := make(map[uint64]uint64)
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return result, err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		var fileSet report.FileSet
		var previous uint64
		for scanner.Scan() {
			line := append(append([]byte(nil), scanner.Bytes()...), '\n')
			parsed, err := record.Parse(line)
			if err != nil {
				result.Malformed++
				continue
			}
			if parsed.RunID != runID {
				result.WrongRunID++
				continue
			}
			fileSet.Records++
			if fileSet.Min == 0 || parsed.Sequence < fileSet.Min {
				fileSet.Min = parsed.Sequence
			}
			if parsed.Sequence > fileSet.Max {
				fileSet.Max = parsed.Sequence
			}
			if previous != 0 && parsed.Sequence < previous {
				result.DescendingTransitions++
			}
			previous = parsed.Sequence
			seen[parsed.Sequence]++
			if seen[parsed.Sequence] > 1 {
				result.Duplicates++
			}
		}
		scanErr := scanner.Err()
		_ = f.Close()
		if scanErr != nil {
			return result, fmt.Errorf("scan %s: %w", path, scanErr)
		}
		result.Files[filepath.Base(path)] = fileSet
	}
	result.ValidUnique = uint64(len(seen))
	for sequence := uint64(1); sequence <= expected; sequence++ {
		if seen[sequence] != 0 {
			continue
		}
		result.Missing++
		if len(result.MissingExamples) < 100 {
			result.MissingExamples = append(result.MissingExamples, sequence)
		}
	}
	return result, nil
}
