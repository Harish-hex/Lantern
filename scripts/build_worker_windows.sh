#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out_dir="$repo_root/bin"
out_file="$out_dir/lantern-worker-windows-amd64.exe"

mkdir -p "$out_dir"

echo "[build-worker] building Windows worker binary..."
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go -C "$repo_root" build -trimpath -ldflags "-s -w" -o "$out_file" ./cmd/lantern-worker

echo "[build-worker] done"
echo "[build-worker] output: $out_file"
