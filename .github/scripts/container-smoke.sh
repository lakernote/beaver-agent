#!/usr/bin/env bash
set -euo pipefail

image="${1:-beaver:test}"
suffix="$$"
container="beaver-smoke-${suffix}"
volume="beaver-smoke-data-${suffix}"

cleanup() {
  docker rm -f "${container}" >/dev/null 2>&1 || true
  docker volume rm -f "${volume}" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

wait_until_ready() {
  local address
  address="$(docker port "${container}" 8080/tcp)"
  for _ in $(seq 1 30); do
    if curl --fail --silent --show-error "http://${address}/api/v1/health" >/dev/null 2>&1; then
      return 0
    fi
    if ! docker inspect --format '{{.State.Running}}' "${container}" 2>/dev/null | grep -qx true; then
      docker logs "${container}" >&2 || true
      return 1
    fi
    sleep 1
  done
  docker logs "${container}" >&2 || true
  return 1
}

start_container() {
  docker run --detach \
    --name "${container}" \
    --mount "type=volume,source=${volume},target=/data" \
    --publish 127.0.0.1::8080 \
    "${image}" >/dev/null
  wait_until_ready
}

docker volume create "${volume}" >/dev/null
start_container
docker exec "${container}" sh -eu -c '
  test "$(id -u)" = "10001"
  test "${HOME}" = "/data"
  test -w /data
  test -f /data/beaver.db
  test -d /data/.beaver/workspaces/default
  printf persisted > /data/smoke-marker
'

docker rm -f "${container}" >/dev/null
start_container
docker exec "${container}" sh -eu -c '
  test "$(cat /data/smoke-marker)" = "persisted"
  test -f /data/beaver.db
'

echo "Container smoke test passed for ${image}"
