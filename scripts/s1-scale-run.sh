#!/usr/bin/env bash
# S1 resource-constrained baseline runner (measurement only).
# Requires: docker compose, built fakemarket:local image.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
OUT="${S1_OUT_DIR:-$ROOT/docs/performance/s1-raw}"
mkdir -p "$OUT"

WARMUP="${S1_WARMUP:-10s}"
MEASURE="${S1_MEASURE:-60s}"
COOLDOWN="${S1_COOLDOWN:-10s}"
REPS="${S1_REPS:-3}"
SEED=42

WORKLOADS=(nomatch fullmatch partial cancel mixed)
RATES=(1000 5000 10000 25000 50000 75000 100000)

echo "=== S1 environment capture ===" | tee "$OUT/env.txt"
{
  echo "date=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "git=$(git rev-parse HEAD)"
  echo "go=$(go version)"
  echo "uname=$(uname -a)"
  echo "docker=$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"
  echo "compose=$(docker compose version 2>/dev/null || true)"
  echo "host_cpus=$(sysctl -n hw.ncpu 2>/dev/null || nproc 2>/dev/null || echo unknown)"
  echo "host_mem=$(sysctl -n hw.memsize 2>/dev/null || free -b 2>/dev/null | awk '/Mem:/{print $2}' || echo unknown)"
  echo "warmup=$WARMUP measure=$MEASURE cooldown=$COOLDOWN reps=$REPS seed=$SEED"
} | tee -a "$OUT/env.txt"

echo "=== build image ==="
docker compose -f docker-compose.scale.yml build

echo "=== start constrained FakeMarket ==="
docker compose -f docker-compose.scale.yml down -v --remove-orphans 2>/dev/null || true
docker compose -f docker-compose.scale.yml up -d fakemarket-scale

# Wait healthy
for i in $(seq 1 60); do
  if curl -sf http://127.0.0.1:18080/ready >/dev/null 2>&1; then
    echo "ready"
    break
  fi
  sleep 1
done
curl -sf http://127.0.0.1:18080/ready | tee "$OUT/ready.json"
echo

# Prove resource limits
{
  echo "=== docker inspect limits ==="
  docker inspect fakemarket-s1 --format 'NanoCpus={{.HostConfig.NanoCpus}} Memory={{.HostConfig.Memory}} GOMAXPROCS env:'
  docker inspect fakemarket-s1 --format '{{range .Config.Env}}{{println .}}{{end}}' | grep -E 'GOMAXPROCS|TRADING|GRPC|WAL' || true
  docker stats fakemarket-s1 --no-stream --format 'cpu={{.CPUPerc}} mem={{.MemUsage}}' || true
} | tee "$OUT/limits.txt"

RESULTS="$OUT/results.jsonl"
: > "$RESULTS"

for w in "${WORKLOADS[@]}"; do
  for rate in "${RATES[@]}"; do
    for rep in $(seq 1 "$REPS"); do
      echo "=== $w rate=$rate rep=$rep ===" | tee -a "$OUT/run.log"
      # Capture container stats mid-run in background
      STATF="$OUT/stats_${w}_${rate}_r${rep}.txt"
      (
        for _ in $(seq 1 12); do
          docker stats fakemarket-s1 --no-stream --format '{{.CPUPerc}} {{.MemUsage}} {{.MemPerc}}' >>"$STATF" 2>/dev/null || true
          sleep 5
        done
      ) &
      STATPID=$!

      set +e
      docker compose -f docker-compose.scale.yml run --rm --no-deps \
        --entrypoint /app/fakemarket-scale scale-gen \
        -target fakemarket-scale:50051 \
        -workload "$w" -rate "$rate" -seed "$SEED" -rep "$rep" \
        -warmup "$WARMUP" -measure "$MEASURE" -cooldown "$COOLDOWN" \
        -clients 8 -instruments 8 -accounts 50 \
        >>"$RESULTS" 2>>"$OUT/run.log"
      RC=$?
      set -e
      kill "$STATPID" 2>/dev/null || true
      wait "$STATPID" 2>/dev/null || true
      echo "exit=$RC" | tee -a "$OUT/run.log"

      # Metrics snapshot
      curl -sf http://127.0.0.1:18080/metrics >"$OUT/metrics_${w}_${rate}_r${rep}.txt" 2>/dev/null || true

      # If OOM/crash, record and continue
      STATUS=$(docker inspect -f '{{.State.Status}} {{.State.OOMKilled}} {{.State.ExitCode}}' fakemarket-s1 2>/dev/null || echo "missing")
      echo "container_status=$STATUS" | tee -a "$OUT/run.log"
      if echo "$STATUS" | grep -q "oom\|false 1\|exited"; then
        if ! docker inspect -f '{{.State.Running}}' fakemarket-s1 2>/dev/null | grep -q true; then
          echo "RESTARTING after failure" | tee -a "$OUT/run.log"
          docker compose -f docker-compose.scale.yml up -d fakemarket-scale
          sleep 5
        fi
      fi
    done
  done
done

echo "=== done; results in $RESULTS ==="
docker compose -f docker-compose.scale.yml down
