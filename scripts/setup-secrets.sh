#!/usr/bin/env bash
# 批量设置仓库 Secrets，或清理已废弃的 Secret。
#
#   cp secrets.env.example secrets.env   # 填好值
#   ./scripts/setup-secrets.sh           # 一次性写进 GitHub（需要 gh CLI）
#   ./scripts/setup-secrets.sh --prune   # 删掉下面 OBSOLETE 里列的那些（有 token 即可）
#
# 后端选择：有 gh CLI 就用 gh；没有但能拿到 token 时，--prune 直接走 REST API
# （删除不需要加密，所以不需要 gh）。写入 Secret 必须用 libsodium sealed box，
# 纯 curl 做不了，仍然要求 gh CLI。
set -euo pipefail

REPO="${REPO:-icezxf/actor-em}"

# 已经不再使用、应该从仓库里清掉的 Secret。
# 代码侧已整体移除，这里保留清单是为了用 --prune 清掉远端的残留项。
OBSOLETE=(
  DEEPL_API_KEY      # 英文简介不再翻译
  ALIST_URL          # alist / nfo 扫描链路已整体移除
  ALIST_USER
  ALIST_PASS
  ALIST_PUBLIC_BASE
  ALIST_DIR
  ALIST_NFO_DIR
  ALIST_NFO_MAX
)

# 取 token：环境变量优先，其次 GitHub Ops 技能里预置的那份
pick_token() {
  for v in "${GITHUB_TOKEN:-}" "${GH_TOKEN:-}"; do
    [ -n "$v" ] && { printf '%s' "$v"; return 0; }
  done
  for f in "$HOME/.workbuddy/skills/github-ops/config/token" "$HOME/.config/gh/token"; do
    [ -f "$f" ] && { tr -d '\r\n' < "$f"; return 0; }
  done
  return 1
}

prune=0
env_file="secrets.env"
for arg in "$@"; do
  case "$arg" in
    --prune) prune=1 ;;
    -*) echo "未知参数: $arg"; exit 2 ;;
    *) env_file="$arg" ;;
  esac
done

have_gh=0
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  have_gh=1
fi
token="$(pick_token || true)"

if [ "$prune" = "1" ]; then
  if [ "$have_gh" = "0" ] && [ -z "$token" ]; then
    echo "既没有 gh CLI 也没有 token，无法清理。装 gh 或设置 GITHUB_TOKEN。"; exit 1
  fi
  echo "清理废弃 Secret（$REPO）："
  for key in "${OBSOLETE[@]}"; do
    if [ "$have_gh" = "1" ]; then
      if gh secret delete "$key" --repo "$REPO" >/dev/null 2>&1; then
        echo "✓ 已删除 $key"
      else
        echo "－ $key 不存在，跳过"
      fi
    else
      code="$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE \
        -H "Authorization: Bearer $token" \
        -H "Accept: application/vnd.github+json" \
        -H "X-GitHub-Api-Version: 2022-11-28" \
        "https://api.github.com/repos/$REPO/actions/secrets/$key")"
      case "$code" in
        204) echo "✓ 已删除 $key" ;;
        404) echo "－ $key 不存在，跳过" ;;
        *)   echo "✗ $key 失败（HTTP $code）" ;;
      esac
    fi
  done
  echo
  echo "剩余：gh secret list --repo $REPO （或 GET /repos/$REPO/actions/secrets）"
  exit 0
fi

[ "$have_gh" = "1" ] || { echo "写入 Secret 需要 gh CLI：https://cli.github.com"; exit 1; }
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
