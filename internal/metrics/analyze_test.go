package metrics

import (
	"math"
	"reflect"
	"testing"

	"logrotate-cache-lab/internal/report"
)

func TestAnalyzeSawtoothRates(t *testing.T) {
	samples := []report.Sample{
		{ElapsedNS: 0, Cache: 10},
		{ElapsedNS: 1e9, Cache: 20},
		{ElapsedNS: 2e9, Cache: 15},
		{ElapsedNS: 3e9, Cache: 35},
	}
	got := Analyze(samples, nil)
	if math.Abs(got.OverallRateBytesPerSecond-25.0/3.0) > 0.0001 {
		t.Fatalf("overall=%f", got.OverallRateBytesPerSecond)
	}
	if math.Abs(got.PositiveRateBytesPerSecond-15) > 0.0001 {
		t.Fatalf("positive=%f", got.PositiveRateBytesPerSecond)
	}
	if math.Abs(got.ReclaimRateBytesPerSecond-5) > 0.0001 {
		t.Fatalf("reclaim=%f", got.ReclaimRateBytesPerSecond)
	}
	if got.MaxBytes != 35 || got.P95Bytes != 35 || got.MedianBytes != 20 {
		t.Fatalf("distribution=%+v", got)
	}
}

func TestAnalyzeEmpty(t *testing.T) {
	if got := Analyze(nil, nil); !reflect.DeepEqual(got, report.CacheMetrics{}) {
		t.Fatalf("got=%+v", got)
	}
}
