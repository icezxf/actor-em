#!/usr/bin/env bash
# 批量设置仓库 Secrets。
#
#   cp secrets.env.example secrets.env   # 填好值
#   ./scripts/setup-secrets.sh           # 一次性写进 GitHub
#
# 前提：已经 gh auth login，且账号对目标仓库有 admin 权限。
set -euo pipefail

REPO="${REPO:-icezxf/actor-em}"
ENV_FILE="${1:-secrets.env}"

command -v gh >/dev/null 2>&1 || { echo "缺少 gh CLI：https://cli.github.com"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "先执行 gh auth login"; exit 1; }
[ -f "$ENV_FILE" ] || { echo "找不到 $ENV_FILE，先 cp secrets.env.example secrets.env"; exit 1; }

ok=0
skip=0
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in \#*) continue ;; esac
  line="$(printf '%s' "$line" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
  [ -z "$line" ] && continue
  key="${line%%=*}"
  val="${line#*=}"
  if [ -z "$key" ] || [ "$key" = "$line" ]; then
    echo "跳过无法解析的行: $line"
    continue
  fi
  if [ -z "$val" ] || [ "$val" = "CHANGE_ME" ]; then
    echo "－ 跳过 $key（未填值）"
    skip=$((skip + 1))
    continue
  fi
  if gh secret set "$key" --body "$val" --repo "$REPO" >/dev/null 2>&1; then
    echo "✓ $key"
    ok=$((ok + 1))
  else
    echo "✗ $key 设置失败"
  fi
done < "$ENV_FILE"

echo
echo "完成：成功 $ok 个，跳过 $skip 个。"
echo "核对：gh secret list --repo $REPO"
