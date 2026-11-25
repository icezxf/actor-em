#!/usr/bin/env python3
"""从 StashDB 拉取演员清单（UUID），供 actor-em 走现有刮削链路。

用法：
    set -a; . /root/.dsh/.env; set +a
    python3 scripts/export_stashdb_list.py                    # 全量女性、排除亚洲
    python3 scripts/export_stashdb_list.py --max 10 -o /tmp/uuid.txt   # 调试：只取 10 个
    python3 scripts/export_stashdb_list.py --include-asia      # 不排除亚洲国家

原理：stash-box GraphQL 的 queryPerformers 分页枚举，gender=FEMALE。
每条拿 id / name / country，本地按国家码过滤掉亚洲，输出一行一个 UUID。
UUID 交给 actorsync 时会走 StashDB 的 findPerformer 精确查询（见 fetch.go）。

环境变量：STASHDB_API_KEY（必填）、STASHDB_ENDPOINT（默认 https://stashdb.org/graphql）
"""

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request

ENDPOINT = os.getenv("STASHDB_ENDPOINT", "https://stashdb.org/graphql").strip()

# 默认排除的亚洲国家/地区码（ erotic 库里日韩居多，已有 actor_dict 覆盖）。
# 空国家码的记录默认保留——可能是欧美演员只是没填国家。
DEFAULT_EXCLUDE = {
    "JP", "KR", "CN", "TW", "HK", "MO", "SG", "TH", "VN", "ID",
    "MY", "PH", "IN", "BD", "PK", "LK", "KH", "LA", "MM", "MN",
}

QUERY = """
query($input: PerformerQueryInput!) {
  queryPerformers(input: $input) {
    count
    performers { id name gender country }
  }
}
"""


def graphql(api_key, variables, retries=4):
    body = json.dumps({"query": QUERY, "variables": {"input": variables}}).encode()
    headers = {
        "Content-Type": "application/json",
        "ApiKey": api_key,
        "User-Agent": "actor-em-list/1.0",
    }
    last = None
    for attempt in range(retries):
        try:
            req = urllib.request.Request(ENDPOINT, data=body, headers=headers, method="POST")
            with urllib.request.urlopen(req, timeout=40) as resp:
                data = json.loads(resp.read().decode("utf-8"))
            if data.get("errors"):
                raise RuntimeError(f"GraphQL errors: {data['errors'][0].get('message')}")
            return data["data"]["queryPerformers"]
        except Exception as e:  # noqa: BLE001 - 网络抖动需重试
            last = e
            if attempt < retries - 1:
                time.sleep(1.5 * (attempt + 1))
    raise RuntimeError(f"请求失败（已重试 {retries} 次）: {last}")


def main():
    ap = argparse.ArgumentParser(description="导出 StashDB 演员 UUID 清单")
    ap.add_argument("-o", "--output", default="actors_stashdb.txt", help="输出文件（默认 actors_stashdb.txt）")
    ap.add_argument("--per-page", type=int, default=200, help="每页条数（默认 200）")
    ap.add_argument("--max", type=int, default=0, help="最多导出多少个（0=不限，调试用小值）")
    ap.add_argument("--include-asia", action="store_true", help="不排除亚洲国家码")
    ap.add_argument("--exclude", default="", help="额外排除的国家码，逗号分隔（如 BR,LATAM）")
    args = ap.parse_args()

    api_key = os.getenv("STASHDB_API_KEY", "").strip()
    if not api_key:
        sys.exit("缺少 STASHDB_API_KEY：先 set -a; . /root/.dsh/.env; set +a")

    exclude = set() if args.include_asia else set(DEFAULT_EXCLUDE)
    for c in args.exclude.split(","):
        if c.strip():
            exclude.add(c.strip().upper())

    # 第一页拿 count
    first = graphql(api_key, {"gender": "FEMALE", "page": 1, "per_page": args.per_page})
    total = first["count"]
    print(f"StashDB 女性演员总数：{total}；每页 {args.per_page}", file=sys.stderr)

    seen = set()
    kept = []
    asia_skipped = 0

    def consume(performers):
        nonlocal asia_skipped
        for p in performers:
            pid = p.get("id", "").strip()
            if not pid or pid in seen:
                continue
            seen.add(pid)
            country = (p.get("country") or "").strip().upper()
            if country in exclude:
                asia_skipped += 1
                continue
            kept.append(pid)

    consume(first["performers"])
    pages = (total + args.per_page - 1) // args.per_page
    for page in range(2, pages + 1):
        if args.max and len(kept) >= args.max:
            break
        batch = graphql(api_key, {"gender": "FEMALE", "page": page, "per_page": args.per_page})
        consume(batch["performers"])
        if page % 20 == 0 or page == pages:
            print(f"  第 {page}/{pages} 页：已留 {len(kept)} 个，跳过亚洲 {asia_skipped} 个", file=sys.stderr)

    if args.max:
        kept = kept[: args.max]

    # 差量合并：已有清单里的 UUID 保留，只追加新抓到的
    if os.path.exists(args.output):
        with open(args.output, encoding="utf-8") as f:
            for line in f:
                v = line.strip()
                if v and v not in seen:
                    seen.add(v)
                    kept.append(v)

    with open(args.output, "w", encoding="utf-8") as f:
        for pid in kept:
            f.write(pid + "\n")

    print(f"已写出 {len(kept)} 个 UUID → {args.output}（亚洲过滤 {asia_skipped} 个）", file=sys.stderr)


if __name__ == "__main__":
    main()
