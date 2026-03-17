#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
git_dir="$(git -C "$repo_root" rev-parse --git-dir)"
lock_dir="$git_dir/auto-verify-push.lock"
log_file="$git_dir/auto-verify-push.log"

if ! mkdir "$lock_dir" 2>/dev/null; then
  exit 0
fi
trap 'rmdir "$lock_dir"' EXIT

branch="$(git -C "$repo_root" branch --show-current)"
if [[ -z "$branch" ]]; then
  {
    printf '[%s] skipped: detached HEAD\n' "$(date -Is)"
  } >>"$log_file"
  exit 0
fi

head_commit="$(git -C "$repo_root" rev-parse HEAD)"

{
  printf '[%s] verifying %s on %s\n' "$(date -Is)" "$head_commit" "$branch"
  if ! git -C "$repo_root" diff --quiet || ! git -C "$repo_root" diff --cached --quiet; then
    echo "skipped: worktree is dirty after commit; refusing to auto-push mixed state"
    exit 0
  fi

  go -C "$repo_root" test ./...

  current_head="$(git -C "$repo_root" rev-parse HEAD)"
  if [[ "$current_head" != "$head_commit" ]]; then
    echo "skipped: HEAD moved during verification"
    exit 0
  fi

  git -C "$repo_root" push origin "$branch"
  printf '[%s] pushed %s to origin/%s\n' "$(date -Is)" "$head_commit" "$branch"
} >>"$log_file" 2>&1
