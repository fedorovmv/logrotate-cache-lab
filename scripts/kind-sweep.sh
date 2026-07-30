#!/usr/bin/env bash
set -euo pipefail

image="${LOGLAB_IMAGE:-logrotate-cache-lab:dev}"
quick=false
if [[ "${1:-}" == "--quick" ]]; then
  quick=true
elif [[ $# -ne 0 ]]; then
  echo "usage: $0 [--quick]" >&2
  exit 2
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
project_dir="$(cd "$script_dir/.." && pwd)"
cluster="loglab-$(date +%H%M%S)-$$"
run_root="$project_dir/results/kind-$(date +%Y%m%dT%H%M%S)"
render_dir="$(mktemp -d)"
timeout_seconds=300

memory_mib=256
max_file_bytes=$((32 * 1024 * 1024))
rotations=3
bytes_per_second=$((8 * 1024 * 1024))
resident_bytes=$((32 * 1024 * 1024))
if $quick; then
  memory_mib=128
  max_file_bytes=$((4 * 1024 * 1024))
  rotations=1
  resident_bytes=$((8 * 1024 * 1024))
  timeout_seconds=120
fi

cleanup() {
  rm -rf "$render_dir"
  kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
}
trap cleanup EXIT

mkdir -p "$run_root"
docker build -t "$image" "$project_dir"
kind create cluster --name "$cluster" --wait 120s
kind load docker-image --name "$cluster" "$image"

wait_for_termination() {
  local job_name=$1
  local deadline=$((SECONDS + timeout_seconds))
  local pod_name=""
  while (( SECONDS < deadline )); do
    pod_name="$(kubectl --context "kind-$cluster" get pods -l "job-name=$job_name" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
    if [[ -n "$pod_name" ]]; then
      local terminated
      terminated="$(kubectl --context "kind-$cluster" get pod "$pod_name" -o jsonpath='{.status.containerStatuses[0].state.terminated.exitCode}' 2>/dev/null || true)"
      if [[ -n "$terminated" ]]; then
        printf '%s' "$pod_name"
        return 0
      fi
    fi
    sleep 1
  done
  return 1
}

failed=0
for strategy in copytruncate rename-reopen; do
  job_name="loglab-$(printf '%s' "$strategy" | tr -d '-')"
  manifest="$render_dir/$strategy.yaml"
  sed \
    -e "s|__JOB_NAME__|$job_name|g" \
    -e "s|__STRATEGY__|$strategy|g" \
    -e "s|__IMAGE__|$image|g" \
    -e "s|__MEMORY_MIB__|$memory_mib|g" \
    -e "s|__MAX_FILE_BYTES__|$max_file_bytes|g" \
    -e "s|__ROTATIONS__|$rotations|g" \
    -e "s|__BYTES_PER_SECOND__|$bytes_per_second|g" \
    -e "s|__RESIDENT_BYTES__|$resident_bytes|g" \
    "$project_dir/deploy/job.yaml" >"$manifest"

  kubectl --context "kind-$cluster" apply -f "$manifest"
  if ! pod_name="$(wait_for_termination "$job_name")"; then
    echo "$strategy: timeout waiting for pod termination" >&2
    kubectl --context "kind-$cluster" get all >"$run_root/$strategy-cluster-state.txt" 2>&1 || true
    failed=1
    continue
  fi

  kubectl --context "kind-$cluster" get pod "$pod_name" -o json >"$run_root/$strategy-pod.json"
  kubectl --context "kind-$cluster" logs "$pod_name" >"$run_root/$strategy.log" 2>&1 || true
  reason="$(kubectl --context "kind-$cluster" get pod "$pod_name" -o jsonpath='{.status.containerStatuses[0].state.terminated.reason}')"
  exit_code="$(kubectl --context "kind-$cluster" get pod "$pod_name" -o jsonpath='{.status.containerStatuses[0].state.terminated.exitCode}')"
  printf '%-14s memory=%sMiB reason=%s exit=%s log=%s\n' "$strategy" "$memory_mib" "$reason" "$exit_code" "$run_root/$strategy.log"
  if [[ "$exit_code" != "0" ]]; then
    failed=1
  fi
done

echo "kind reports: $run_root"
exit "$failed"
