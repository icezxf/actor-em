#!/usr/bin/env python3
"""从 StashDB / JavStash 按人气（POPULARITY DESC）拉全量排名，生成排名文件。

输出：
    data/popularity_stash.json   { "<stashdb uuid>": rank, ... }   rank 从 1 开始
    data/popularity_jav.json     { "<javstash uuid>": rank, ... }

说明：
  - 两站的 Performer 类型都没有 popularity 数值字段，但 queryPerformers 支持
    sort:POPULARITY / direction:DESC，排名 = 返回列表中的位置。
  - JavStash 必须用自定义 User-Agent，否则 Cloudflare 返回 403 error 1010。
  - 用 curl 子进程发请求（最稳，能设 UA），失败重试。

用法：
    set -a; . /root/.dsh/.env; set +a
    python3 scripts/export_popularity.py
    python3 scripts/export_popularity.py --only stash     # 只拉一站
    python3 scripts/export_popularity.py --per-page 500 --max 2000   # 调试
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
UA = "actor-em/1.0 (https://github.com/icezxf/actor-em)"

QUERY = """query($i: PerformerQueryInput!) {
  queryPerformers(input: $i) { count performers { id } }
}"""

SITES = {
    "stash": {
        "env": "STASHDB_API_KEY",
        "url": "https://stashdb.org/graphql",
        "out": "data/popularity_stash.json",
    },
    "jav": {
        "env": "JAVSTASH_API_KEY",
        "url": "https://javstash.org/graphql",
        "out": "data/popularity_jav.json",
    },
}


def graphql(url: str, api_key: str, variables: dict, retries: int = 5) -> dict:
    body = json.dumps({"query": QUERY, "variables": {"i": variables}}, ensure_ascii=False)
    last = ""
    for attempt in range(retries):
        # 用 curl：能可靠设置 UA，Python urllib 的默认 UA 会被 JavStash 的 CF 拦截
        cmd = [
            "curl", "-s", "--ipv4", "--max-time", "40", url,
            "-H", "Content-Type: application/json",
            "-H", f"ApiKey: {api_key}",
            "-H", f"User-Agent: {UA}",
            "--data-binary", body,
        ]
        try:
            raw = subprocess.run(cmd, capture_output=True, text=True, timeout=50).stdout
            if not raw or not raw.strip():
                last = "empty response"
                time.sleep(2 * (attempt + 1))
                continue
            data = json.loads(raw)
        except Exception as e:
            last = f"{type(e).__name__}: {e}"
            time.sleep(2 * (attempt + 1))
            continue
        if data.get("errors"):
            last = data["errors"][0].get("message", str(data["errors"][0]))
            time.sleep(2 * (attempt + 1))
            continue
        result = (data.get("data") or {}).get("queryPerformers")
        if result is None:
            last = "no queryPerformers in response"
            time.sleep(2)
            continue
        return result
    raise RuntimeError(f"GraphQL 失败（重试 {retries} 次）: {last}")


def export_site(name: str, cfg: dict, per_page: int, max_n: int) -> tuple[int, str]:
    api_key = os.getenv(cfg["env"], "").strip()
    if not api_key:
        print(f"[{name}] 跳过：缺少 {cfg['env']}", file=sys.stderr)
        return 0, cfg["out"]

    first = graphql(cfg["url"], api_key, {
        "sort": "POPULARITY", "direction": "DESC", "page": 1, "per_page": per_page,
    })
    total = first["count"]
    print(f"[{name}] 总数 {total}，每页 {per_page}", file=sys.stderr)

    ranks: dict[str, int] = {}
    seen: set[str] = set()
    rank = 0

    def consume(performers):
        nonlocal rank
        for p in performers:
            pid = (p.get("id") or "").strip()
            if not pid or pid in seen:
                continue
            seen.add(pid)
            rank += 1
            ranks[pid] = rank

    consume(first["performers"])
    if max_n and rank >= max_n:
        ranks = {k: v for k, v in ranks.items() if v <= max_n}
    else:
        pages = (total + per_page - 1) // per_page
        for page in range(2, pages + 1):
            if max_n and rank >= max_n:
                break
            batch = graphql(cfg["url"], api_key, {
                "sort": "POPULARITY", "direction": "DESC", "page": page, "per_page": per_page,
            })
            consume(batch["performers"])
            if page % 20 == 0 or page == pages:
                print(f"[{name}] {page}/{pages} 页，已记 {rank} 个", file=sys.stderr)

    out_path = os.path.join(ROOT, cfg["out"])
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    with open(out_path, "w", encoding="utf-8") as f:
        json.dump(ranks, f, ensure_ascii=False, separators=(",", ":"))
        f.write("\n")
    size = os.path.getsize(out_path)
    print(f"[{name}] {len(ranks)} 个排名 → {out_path} ({size/1024:.0f} KB)", file=sys.stderr)
    return len(ranks), cfg["out"]


def main():
    ap = argparse.ArgumentParser(description="导出 StashDB/JavStash 人气排名")
    ap.add_argument("--only", choices=["stash", "jav", "both"], default="both")
    ap.add_argument("--per-page", type=int, default=200)
    ap.add_argument("--max", type=int, default=0, help="调试：每站最多记多少个（0=全量）")
    args = ap.parse_args()

    sites = SITES if args.only == "both" else {args.only: SITES[args.only]}
    for name, cfg in sites.items():
        export_site(name, cfg, args.per_page, args.max)


if __name__ == "__main__":
    main()
