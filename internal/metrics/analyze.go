package metrics

import (
	"math"
	"sort"

	"logrotate-cache-lab/internal/report"
)

func Analyze(samples []report.Sample, events []report.RotationEvent) report.CacheMetrics {
	if len(samples) == 0 {
		return report.CacheMetrics{}
	}
	values := make([]uint64, len(samples))
	var sum float64
	var positiveBytes, reclaimBytes float64
	var positiveSeconds, reclaimSeconds float64
	max := samples[0].Cache
	for i, sample := range samples {
		values[i] = sample.Cache
		sum += float64(sample.Cache)
		if sample.Cache > max {
			max = sample.Cache
		}
		if i == 0 {
			continue
		}
		dt := float64(sample.ElapsedNS-samples[i-1].ElapsedNS) / 1e9
		if dt <= 0 {
			continue
		}
		if sample.Cache >= samples[i-1].Cache {
			positiveBytes += float64(sample.Cache - samples[i-1].Cache)
			positiveSeconds += dt
		} else {
			reclaimBytes += float64(samples[i-1].Cache - sample.Cache)
			reclaimSeconds += dt
		}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	duration := float64(samples[len(samples)-1].ElapsedNS-samples[0].ElapsedNS) / 1e9
	overall := 0.0
	if duration > 0 {
		overall = (float64(samples[len(samples)-1].Cache) - float64(samples[0].Cache)) / duration
	}
	result := report.CacheMetrics{
		StartBytes:                 samples[0].Cache,
		EndBytes:                   samples[len(samples)-1].Cache,
		MaxBytes:                   max,
		MeanBytes:                  sum / float64(len(samples)),
		MedianBytes:                values[len(values)/2],
		P95Bytes:                   values[nearestRank(len(values), 0.95)],
		OverallRateBytesPerSecond:  overall,
		PositiveRateBytesPerSecond: divide(positiveBytes, positiveSeconds),
		ReclaimRateBytesPerSecond:  divide(reclaimBytes, reclaimSeconds),
	}
	result.RotationTransientMaxBytes = rotationTransient(samples, events)
	result.RotationIntervalSlopes = intervalSlopes(samples, events)
	return result
}

func divide(n, d float64) float64 {
	if d == 0 {
		return 0
	}
	return n / d
}

func nearestRank(n int, p float64) int {
	rank := int(math.Ceil(p*float64(n))) - 1
	if rank < 0 {
		return 0
	}
	if rank >= n {
		return n - 1
	}
	return rank
}

func rotationTransient(samples []report.Sample, events []report.RotationEvent) uint64 {
	var max uint64
	for _, start := range events {
		if start.Phase != "copy-start" && start.Phase != "rename" {
			continue
		}
		end := int64(math.MaxInt64)
		for _, event := range events {
			if event.Ordinal == start.Ordinal && event.Phase == "retention-complete" && event.ElapsedNS >= start.ElapsedNS {
				end = event.ElapsedNS
				break
			}
		}
		var base, peak uint64
		found := false
		for _, sample := range samples {
			if sample.ElapsedNS >= start.ElapsedNS && sample.ElapsedNS <= end {
				if !found {
					base, peak, found = sample.Cache, sample.Cache, true
				}
				if sample.Cache > peak {
					peak = sample.Cache
				}
			}
		}
		if found && peak > base && peak-base > max {
			max = peak - base
		}
	}
	return max
}

func intervalSlopes(samples []report.Sample, events []report.RotationEvent) []float64 {
	boundaries := []int64{samples[0].ElapsedNS}
	for _, event := range events {
		if event.Phase == "retention-complete" {
			boundaries = append(boundaries, event.ElapsedNS)
		}
	}
	boundaries = append(boundaries, samples[len(samples)-1].ElapsedNS)
	sort.Slice(boundaries, func(i, j int) bool { return boundaries[i] < boundaries[j] })
	var out []float64
	for i := 1; i < len(boundaries); i++ {
		var segment []report.Sample
		for _, sample := range samples {
			if sample.ElapsedNS >= boundaries[i-1] && sample.ElapsedNS <= boundaries[i] {
				segment = append(segment, sample)
			}
		}
		if len(segment) >= 2 {
			out = append(out, leastSquares(segment))
		}
	}
	return out
}

func leastSquares(samples []report.Sample) float64 {
	var sx, sy, sxy, sxx float64
	for _, sample := range samples {
		x := float64(sample.ElapsedNS) / 1e9
		y := float64(sample.Cache)
		sx, sy, sxy, sxx = sx+x, sy+y, sxy+x*y, sxx+x*x
	}
	n := float64(len(samples))
	return divide(n*sxy-sx*sy, n*sxx-sx*sx)
}
