#!/usr/bin/env bash
set -euo pipefail

image="${LOGLAB_IMAGE:-logrotate-cache-lab:dev}"
quick=false
if [[ "${1:-}" == "--quick" ]]; then
  quick=true
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(cd "$script_dir/.." && pwd)"
run_root="$project_dir/results/compare-$(date +%Y%m%dT%H%M%S)"
mkdir -p "$run_root"

docker build -t "$image" "$project_dir"

max_file_bytes=$((32 * 1024 * 1024))
rotations=4
rate=$((8 * 1024 * 1024))
resident=$((32 * 1024 * 1024))
if $quick; then
  max_file_bytes=$((4 * 1024 * 1024))
  rotations=2
  rate=$((8 * 1024 * 1024))
  resident=$((8 * 1024 * 1024))
fi

volumes=()
cleanup() {
  for volume in "${volumes[@]:-}"; do
    docker volume rm -f "$volume" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

summaries=()
for strategy in copytruncate rename-reopen; do
  volume="loglab-compare-${strategy}-$$"
  volumes+=("$volume")
  docker volume create "$volume" >/dev/null
  result_dir="$run_root/$strategy"
  mkdir -p "$result_dir"
  chmod 0777 "$result_dir"
  docker run --rm \
    --mount "source=$volume,target=/var/log/loglab" \
    --mount "type=bind,source=$result_dir,target=/results" \
    "$image" run \
    --run-id "$strategy" \
    --strategy "$strategy" \
    --max-file-bytes "$max_file_bytes" \
    --rotations "$rotations" \
    --bytes-per-second "$rate" \
    --resident-bytes "$resident" >/dev/null
  summaries+=("$result_dir/summary.json")
done

python3 - "${summaries[@]}" "$run_root/comparison.json" <<'PY'
import json, sys
paths, output = sys.argv[1:-1], sys.argv[-1]
runs = [json.load(open(path, encoding="utf-8")) for path in paths]
with open(output, "w", encoding="utf-8") as stream:
    json.dump({"schema_version": 1, "runs": runs}, stream, indent=2)
print("strategy       cache-fill MiB/s  peak-cache MiB  peak-memory MiB  missing  duplicates")
for run in runs:
    cache = run.get("cache", {})
    integ = run.get("integrity", {})
    print(f"{run['strategy']:<14} {cache.get('positive_rate_bytes_per_second', 0)/1048576:>18.2f} "
          f"{cache.get('max_bytes', 0)/1048576:>15.2f} {run.get('peak_memory', 0)/1048576:>16.2f} "
          f"{integ.get('missing', 0):>8} {integ.get('duplicates', 0):>11}")
print(f"reports: {output}")
PY
