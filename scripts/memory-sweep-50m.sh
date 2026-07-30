#!/usr/bin/env bash
set -euo pipefail

image="${LOGLAB_IMAGE:-logrotate-cache-lab:dev}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(cd "$script_dir/.." && pwd)"
quick=false
if [[ "${1:-}" == "--quick" ]]; then
  quick=true
elif [[ $# -ne 0 ]]; then
  echo "usage: $0 [--quick]" >&2
  exit 2
fi

step_mib=4
repetitions=3
upper_mib=192
if $quick; then
  step_mib=16
  repetitions=1
  upper_mib=160
fi

docker build -t "$image" "$project_dir"
mkdir -p "$project_dir/results"

cd "$project_dir"
go run ./cmd/loglab memory-sweep \
  --image "$image" \
  --strategy both \
  --lower-mib 32 \
  --upper-mib "$upper_mib" \
  --step-mib "$step_mib" \
  --repetitions "$repetitions" \
  --max-file-bytes $((50 * 1024 * 1024)) \
  --rotations 1 \
  --record-bytes 512 \
  --bytes-per-second $((8 * 1024 * 1024)) \
  --buffer-bytes $((64 * 1024)) \
  --flush-interval 100ms \
  --monitor-interval 100ms \
  --resident-bytes $((32 * 1024 * 1024)) \
  --attempt-timeout 45s \
  --result-root "$project_dir/results/sweep-50m-attempts" \
  --output "$project_dir/results/memory-sweep-50m.json"
