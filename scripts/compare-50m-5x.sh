#!/usr/bin/env bash
set -euo pipefail

image="${LOGLAB_IMAGE:-logrotate-cache-lab:dev}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(cd "$script_dir/.." && pwd)"
run_root="$project_dir/results/compare-50m-5x-$(date +%Y%m%dT%H%M%S)"
mkdir -p "$run_root"

docker build -t "$image" "$project_dir"

volumes=()
cleanup() {
  for volume in "${volumes[@]:-}"; do
    docker volume rm -f "$volume" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

for strategy in copytruncate rename-reopen; do
  volume="loglab-50m-5x-${strategy}-$$"
  volumes+=("$volume")
  docker volume create "$volume" >/dev/null
  result_dir="$run_root/$strategy"
  mkdir -p "$result_dir"
  chmod 0777 "$result_dir"
  docker run --rm \
    --memory=768m \
    --memory-swap=768m \
    --mount "source=$volume,target=/var/log/loglab" \
    --mount "type=bind,source=$result_dir,target=/results" \
    "$image" run \
    --run-id "50m-5x-$strategy" \
    --strategy "$strategy" \
    --max-file-bytes $((50 * 1024 * 1024)) \
    --rotations 5 \
    --max-backups 5 \
    --record-bytes 512 \
    --bytes-per-second $((8 * 1024 * 1024)) \
    --buffer-bytes $((64 * 1024)) \
    --flush-interval 100ms \
    --monitor-interval 20ms \
    --resident-bytes $((32 * 1024 * 1024)) >/dev/null
done

python3 - "$run_root" <<'PY'
import csv
import html
import json
import math
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
strategies = {
    "copytruncate": {"color": "#f28c28", "label": "copytruncate"},
    "rename-reopen": {"color": "#138a5b", "label": "rename + reopen"},
}
runs = []
series = {}
events = {}
for strategy in strategies:
    summary = json.loads((root / strategy / "summary.json").read_text())
    runs.append(summary)
    with (root / strategy / "samples.csv").open(newline="") as stream:
        series[strategy] = [
            (int(row["elapsed_ns"]) / 1e9, int(row["cache"]) / 1048576)
            for row in csv.DictReader(stream)
        ]
    with (root / strategy / "events.csv").open(newline="") as stream:
        events[strategy] = [
            int(row["elapsed_ns"]) / 1e9
            for row in csv.DictReader(stream)
            if row["phase"] == "retention-complete"
        ]

(root / "comparison.json").write_text(json.dumps({"schema_version": 1, "runs": runs}, indent=2) + "\n")

width, height = 1000, 500
left, right, top, bottom = 75, 30, 45, 65
plot_w, plot_h = width - left - right, height - top - bottom
max_x = max(points[-1][0] for points in series.values())
max_cache = max(value for points in series.values() for _, value in points)
max_y = max(50, math.ceil(max_cache / 50) * 50)

def x(value): return left + value / max_x * plot_w
def y(value): return top + (1 - value / max_y) * plot_h

svg = [
    f'<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="0 0 {width} {height}">',
    '<rect width="100%" height="100%" rx="18" fill="#ffffff"/>',
    '<text x="75" y="27" font-family="Inter,system-ui,sans-serif" font-size="18" font-weight="700" fill="#172033">File cache: 5 ротаций × 50 MiB, max-backups=5</text>',
]
for tick in range(0, int(max_y) + 1, 50):
    yy = y(tick)
    svg.append(f'<line x1="{left}" y1="{yy:.1f}" x2="{width-right}" y2="{yy:.1f}" stroke="#d9e1ec"/>')
    svg.append(f'<text x="{left-12}" y="{yy+4:.1f}" text-anchor="end" font-family="Inter,system-ui,sans-serif" font-size="12" fill="#667085">{tick} MiB</text>')
for tick in range(0, int(max_x) + 1, 5):
    xx = x(tick)
    svg.append(f'<line x1="{xx:.1f}" y1="{top}" x2="{xx:.1f}" y2="{height-bottom}" stroke="#eef2f7"/>')
    svg.append(f'<text x="{xx:.1f}" y="{height-bottom+25}" text-anchor="middle" font-family="Inter,system-ui,sans-serif" font-size="12" fill="#667085">{tick}s</text>')

for strategy, config in strategies.items():
    for ordinal, elapsed in enumerate(events[strategy], 1):
        xx = x(elapsed)
        svg.append(f'<line x1="{xx:.1f}" y1="{top}" x2="{xx:.1f}" y2="{height-bottom}" stroke="{config["color"]}" stroke-dasharray="4 5" opacity="0.22"/>')
        svg.append(f'<text x="{xx+3:.1f}" y="{top+14}" font-family="Inter,system-ui,sans-serif" font-size="10" fill="{config["color"]}" opacity="0.8">R{ordinal}</text>')
    points = " ".join(f"{x(elapsed):.1f},{y(cache):.1f}" for elapsed, cache in series[strategy])
    svg.append(f'<polyline points="{points}" fill="none" stroke="{config["color"]}" stroke-width="3" stroke-linejoin="round" stroke-linecap="round"/>')

legend_x = width - 350
for index, (strategy, config) in enumerate(strategies.items()):
    yy = 22 + index * 20
    svg.append(f'<line x1="{legend_x}" y1="{yy}" x2="{legend_x+28}" y2="{yy}" stroke="{config["color"]}" stroke-width="4"/>')
    svg.append(f'<text x="{legend_x+36}" y="{yy+4}" font-family="Inter,system-ui,sans-serif" font-size="12" fill="#172033">{html.escape(config["label"])}</text>')
svg.append('</svg>')
(root / "cache-timeseries.svg").write_text("\n".join(svg) + "\n")

print("strategy       peak-cache MiB  end-cache MiB  peak-memory MiB  fill MiB/s  missing")
for run in runs:
    cache = run["cache"]
    integrity = run["integrity"]
    print(f"{run['strategy']:<14} {cache['max_bytes']/1048576:>14.2f} {cache['end_bytes']/1048576:>14.2f} "
          f"{run['peak_memory']/1048576:>16.2f} {cache['positive_rate_bytes_per_second']/1048576:>11.2f} "
          f"{integrity['missing']:>8}")
print(f"report: {root / 'comparison.json'}")
print(f"chart:  {root / 'cache-timeseries.svg'}")
PY
