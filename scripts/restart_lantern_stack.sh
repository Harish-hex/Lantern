#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bin_path="$repo_root/bin/lantern"

host="127.0.0.1"
port="9723"
http_port="9724"
worker_id="worker-1"
build_first="false"
start_worker="true"

usage() {
  cat <<'USAGE'
Usage: scripts/restart_lantern_stack.sh [options]

Restarts Lantern server (and optional worker) from the nested repo binary.
Always targets ./bin/lantern under the current Lantern repository.

Options:
  --build                 Rebuild binary before restart
  --host <host>           Worker host for server connection (default: 127.0.0.1)
  --port <port>           TCP server port (default: 9723)
  --http-port <port>      HTTP dashboard port (default: 9724)
  --worker-id <id>        Worker ID (default: worker-1)
  --no-worker             Start only server
  --help                  Show this message
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --build)
      build_first="true"
      shift
      ;;
    --host)
      host="$2"
      shift 2
      ;;
    --port)
      port="$2"
      shift 2
      ;;
    --http-port)
      http_port="$2"
      shift 2
      ;;
    --worker-id)
      worker_id="$2"
      shift 2
      ;;
    --no-worker)
      start_worker="false"
      shift
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [[ "$build_first" == "true" ]]; then
  echo "[lantern-restart] building binary..."
  go -C "$repo_root" build -o "$bin_path" ./cmd/lantern
fi

if [[ ! -x "$bin_path" ]]; then
  echo "[lantern-restart] binary not found: $bin_path" >&2
  echo "[lantern-restart] run with --build or build manually first" >&2
  exit 1
fi

runtime_dir="$repo_root/.lantern-runtime"
mkdir -p "$runtime_dir"

server_log="$runtime_dir/server.log"
worker_log="$runtime_dir/worker.log"
server_pid_file="$runtime_dir/server.pid"
worker_pid_file="$runtime_dir/worker.pid"

stop_existing() {
  local pids=""

  pids="$(lsof -tiTCP:"$http_port" -sTCP:LISTEN || true)"
  if [[ -n "$pids" ]]; then
    echo "[lantern-restart] stopping HTTP listeners on :$http_port ($pids)"
    echo "$pids" | xargs kill
  fi

  pids="$(lsof -tiTCP:"$port" -sTCP:LISTEN || true)"
  if [[ -n "$pids" ]]; then
    echo "[lantern-restart] stopping TCP listeners on :$port ($pids)"
    echo "$pids" | xargs kill
  fi

  pids="$(ps aux | awk '/[l]antern worker/ {print $2}' || true)"
  if [[ -n "$pids" ]]; then
    echo "[lantern-restart] stopping worker processes ($pids)"
    echo "$pids" | xargs kill
  fi

  sleep 1
}

wait_for_http() {
  local attempts=30
  local i=0
  while (( i < attempts )); do
    if curl -fsS "http://127.0.0.1:${http_port}/api/compute/templates" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.5
    ((i+=1))
  done
  return 1
}

stop_existing

echo "[lantern-restart] starting server from $bin_path"
nohup "$bin_path" serve --port "$port" --http-port "$http_port" >"$server_log" 2>&1 &
server_pid=$!
echo "$server_pid" >"$server_pid_file"

if wait_for_http; then
  echo "[lantern-restart] server is up on http://127.0.0.1:$http_port"
else
  echo "[lantern-restart] server did not become ready; check $server_log" >&2
  exit 1
fi

if [[ "$start_worker" == "true" ]]; then
  echo "[lantern-restart] starting worker $worker_id"
  nohup "$bin_path" worker --host "$host" --port "$port" --http-port "$http_port" --worker-id "$worker_id" >"$worker_log" 2>&1 &
  worker_pid=$!
  echo "$worker_pid" >"$worker_pid_file"
fi

echo "[lantern-restart] done"
echo "  server pid: $(cat "$server_pid_file")"
if [[ "$start_worker" == "true" ]]; then
  echo "  worker pid: $(cat "$worker_pid_file")"
fi
echo "  server log: $server_log"
if [[ "$start_worker" == "true" ]]; then
  echo "  worker log: $worker_log"
fi
