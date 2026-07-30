#!/usr/bin/env bash
set -euo pipefail

image="${LOGLAB_IMAGE:-logrotate-cache-lab:dev}"
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(cd "$script_dir/.." && pwd)"
quick=false
if [[ "${1:-}" == "--quick" ]]; then
  quick=true
fi

docker build -t "$image" "$project_dir"
mkdir -p "$project_dir/results"

args=(
  --image "$image"
  --strategy both
  --result-root "$project_dir/results/sweep-attempts"
  --output "$project_dir/results/memory-sweep.json"
)
if $quick; then
  args+=(
    --lower-mib 16
    --upper-mib 128
    --step-mib 8
    --repetitions 1
    --max-file-bytes $((4 * 1024 * 1024))
    --rotations 1
    --bytes-per-second $((8 * 1024 * 1024))
    --resident-bytes $((8 * 1024 * 1024))
	--attempt-timeout 15s
  )
fi

cd "$project_dir"
go run ./cmd/loglab memory-sweep "${args[@]}"
