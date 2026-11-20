#!/usr/bin/env bash
# 批量设置仓库 Secrets，或清理已废弃的 Secret。
#
#   cp secrets.env.example secrets.env   # 填好值
#   ./scripts/setup-secrets.sh           # 一次性写进 GitHub
#   ./scripts/setup-secrets.sh --prune   # 删掉下面 OBSOLETE 里列的那些
#
# 前提：已经 gh auth login，且账号对目标仓库有 admin 权限。
set -euo pipefail

REPO="${REPO:-icezxf/actor-em}"

# 已经不再使用、应该从仓库里清掉的 Secret。
# 留着不会出错，但会让人以为这些还得配（尤其 ALIST_* 在新链路里完全没用）。
OBSOLETE=(
  DEEPL_API_KEY      # 英文简介不再翻译
  ALIST_URL          # 头像不转存到 alist
  ALIST_USER
  ALIST_PASS
  ALIST_PUBLIC_BASE  # 不再上传，没有直链要拼
  ALIST_NFO_DIR      # build-actor-list 已停用
  ALIST_NFO_MAX
)

prune=0
env_file="secrets.env"
for arg in "$@"; do
  case "$arg" in
    --prune) prune=1 ;;
    -*) echo "未知参数: $arg"; exit 2 ;;
    *) env_file="$arg" ;;
  esac
done

command -v gh >/dev/null 2>&1 || { echo "缺少 gh CLI：https://cli.github.com"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "先执行 gh auth login"; exit 1; }

if [ "$prune" = "1" ]; then
  echo "清理废弃 Secret（$REPO）："
  for key in "${OBSOLETE[@]}"; do
    if gh secret delete "$key" --repo "$REPO" >/dev/null 2>&1; then
      echo "✗ 已删除 $key"
    else
      echo "－ $key 不存在，跳过"
    fi
  done
  echo
  echo "剩余：gh secret list --repo $REPO"
  exit 0
fi

[ -f "$env_file" ] || { echo "找不到 $env_file，先 cp secrets.env.example secrets.env"; exit 1; }

ok=0
skip=0
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in \#*) continue ;; esac
  line="$(printf '%s' "$line" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
  [ -z "$line" ] && continue
  key="${line%%=*}"
  val="${line#*=}"
  # 剥掉「空白 + #」开头的行尾注释；值内部出现 # 不会被误切
  val="$(printf '%s' "$val" | sed 's/[[:space:]]#.*$//; s/[[:space:]]*$//')"
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
done < "$env_file"

echo
echo "完成：成功 $ok 个，跳过 $skip 个。"
echo "核对：gh secret list --repo $REPO"
echo "清理废弃项：./scripts/setup-secrets.sh --prune"
