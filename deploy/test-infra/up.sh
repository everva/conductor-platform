#!/usr/bin/env bash
# Bring up the optiway test-infra on the current host (everva) and wait until every
# service is healthy. Idempotent.
set -euo pipefail
cd "$(dirname "$0")"

PROJECT=optiway-testinfra

echo "[test-infra] building + starting..."
docker compose -p "$PROJECT" -f docker-compose.yml up -d --build

echo "[test-infra] waiting for health..."
deadline=$(( $(date +%s) + 240 ))
while :; do
  unhealthy=$(docker compose -p "$PROJECT" -f docker-compose.yml ps --format '{{.Service}} {{.Health}}' \
    | awk '$1!="temporal-admin" && $2!="" && $2!="healthy" {print $1}')
  if [ -z "$unhealthy" ]; then
    echo "[test-infra] all services healthy"
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    echo "[test-infra] TIMEOUT waiting for: $unhealthy" >&2
    docker compose -p "$PROJECT" -f docker-compose.yml ps
    exit 1
  fi
  sleep 5
done

docker compose -p "$PROJECT" -f docker-compose.yml ps
