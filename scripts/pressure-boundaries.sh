#!/usr/bin/env bash
set -euo pipefail

image="${LOGLAB_IMAGE:-logrotate-cache-lab:dev}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(cd "$script_dir/.." && pwd)"
run_root="$project_dir/results/pressure-boundaries-$(date +%Y%m%dT%H%M%S)"
repetitions=3
if [[ "${1:-}" == "--quick" ]]; then
  repetitions=1
elif [[ $# -ne 0 ]]; then
  echo "usage: $0 [--quick]" >&2
  exit 2
fi

docker build -t "$image" "$project_dir"
mkdir -p "$run_root"

run_case() {
  local label=$1 strategy=$2 file_mib=$3 rotations=$4 backups=$5 lower=$6 upper=$7 timeout=$8
  local output="$run_root/$label.json"
  echo "running $label: strategy=$strategy file=${file_mib}MiB rotations=$rotations limits=${lower}/${upper}MiB repetitions=$repetitions"
  set +e
  go run ./cmd/loglab memory-sweep \
    --image "$image" \
    --strategy "$strategy" \
    --lower-mib "$lower" \
    --upper-mib "$upper" \
    --step-mib 4 \
    --repetitions "$repetitions" \
    --max-file-bytes $((file_mib * 1024 * 1024)) \
    --rotations "$rotations" \
    --max-backups "$backups" \
    --record-bytes 512 \
    --bytes-per-second $((8 * 1024 * 1024)) \
    --buffer-bytes $((64 * 1024)) \
    --flush-interval 100ms \
    --monitor-interval 20ms \
    --resident-bytes $((32 * 1024 * 1024)) \
    --attempt-timeout "$timeout" \
    --result-root "$run_root/$label-attempts" \
    --output "$output"
  local status=$?
  set -e
  if [[ ! -s "$output" ]]; then
    echo "$label did not produce a report" >&2
    return 1
  fi
  if [[ $status -ne 0 ]]; then
    echo "$label did not resolve inside the requested limits; retaining partial evidence" >&2
  fi
}

cd "$project_dir"
# Adjacent 4 MiB candidates established by the exploratory runs. Keeping both
# the known failing and passing candidate makes regressions in reclaim behavior
# visible instead of reporting only an upper bound.
run_case 50m-1x-copy copytruncate 50 1 1 64 68 45s
run_case 100m-1x-copy copytruncate 100 1 1 68 72 60s
run_case 100m-1x-baseline baseline 100 1 1 52 56 60s
run_case 100m-1x-rename rename-reopen 100 1 1 52 56 60s
run_case 200m-1x-copy copytruncate 200 1 1 68 72 90s
run_case 200m-1x-baseline baseline 200 1 1 52 56 90s
run_case 200m-1x-rename rename-reopen 200 1 1 52 56 90s
run_case 50m-5x-copy copytruncate 50 5 5 64 68 90s
run_case 50m-5x-rename rename-reopen 50 5 5 52 56 90s

python3 - "$run_root" <<'PY'
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
cases = []
for path in sorted(root.glob("*.json")):
    if path.name == "pressure-boundaries.json":
        continue
    document = json.loads(path.read_text())
    for report in document.get("reports", []):
        cases.append({"case": path.stem, "report": report})

output = {"schema_version": 1, "cases": cases}
(root / "pressure-boundaries.json").write_text(json.dumps(output, indent=2) + "\n")

print("case                 boundary                  attempts  oom  functional  integrity")
for case in cases:
    report = case["report"]
    if report.get("boundary_resolved"):
        boundary = f"{report['greatest_fail_mib']} fail / {report['minimum_pass_mib']} pass"
    elif report.get("minimum_pass_mib"):
        boundary = f"<= {report['minimum_pass_mib']} pass"
    else:
        boundary = f"> {report['greatest_fail_mib']} (upper failed)"
    print(f"{case['case']:<20} {boundary:<25} {len(report.get('attempts', [])):>8} "
          f"{report.get('oom_failures', 0):>4} {report.get('functional_failures', 0):>11} "
          f"{report.get('integrity_failures', 0):>10}")
print(f"report: {root / 'pressure-boundaries.json'}")
PY
