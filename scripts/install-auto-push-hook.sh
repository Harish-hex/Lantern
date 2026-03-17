#!/usr/bin/env bash
set -euo pipefail

# Finds the repo root and installs the verify-and-push hook into .git/hooks.
repo_root="$(git rev-parse --show-toplevel)"
hook_src="$repo_root/scripts/git-hooks/post-commit"
hook_target="$(git -C "$repo_root" rev-parse --git-dir)/hooks/post-commit"

if [ ! -f "$hook_src" ]; then
  echo "hook source $hook_src is missing" >&2
  exit 1
fi

if [ -f "$hook_target" ]; then
  backup="${hook_target}.bak-$(date +%s)"
  echo "existing hook detected; backing up to $backup"
  cp "$hook_target" "$backup"
fi

cp "$hook_src" "$hook_target"
chmod +x "$hook_target"

printf "Installed auto push hook to %s\n" "$hook_target"
printf "Run %s manually once to confirm tests pass before relying on automatic pushes.\n" "$repo_root/scripts/verify-and-push.sh"
