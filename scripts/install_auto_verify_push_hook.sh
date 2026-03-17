#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
hooks_dir="$(git -C "$repo_root" rev-parse --git-dir)/hooks"
hook_path="$hooks_dir/post-commit"

mkdir -p "$hooks_dir"
cat >"$hook_path" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
nohup "$repo_root/scripts/auto_verify_push.sh" >/dev/null 2>&1 &
EOF

chmod +x "$hook_path"
echo "Installed post-commit hook at $hook_path"
echo "Log file: $(git -C "$repo_root" rev-parse --git-dir)/auto-verify-push.log"
