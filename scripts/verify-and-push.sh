#!/usr/bin/env bash
set -euo pipefail

# This helper validates that the working tree is clean, runs the repo's tests,
# and pushes the current branch if all checks pass.
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$repo_root"

if [ -n "$(git status --porcelain)" ]; then
  echo "working tree is not clean; commit or stash changes before auto-pushing" >&2
  git status --short
  exit 1
fi

current_branch="$(git rev-parse --abbrev-ref HEAD)"
printf "Running tests on branch %s...\n" "$current_branch"
go test ./...

printf "Tests passed; pushing %s to origin\n" "$current_branch"
git push
