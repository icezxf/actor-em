#!/usr/bin/env python3
"""生成「候选头像」分片索引：从 StashDB / JavStash 拉每个演员的所有图。

为什么要预生成而不是前端实时拉：
  - 两站的 GraphQL 都要 API key，放前端会泄露；
  - 起一个 Worker 代理当然也行，但静态站保持零后端更简单、更快；
  - 每人图数有限（实测 6~21 张），压成按名字/ID 分片的静态文件，点开某人只拉一片。

输出：
    site/data/avatars_stash/<shard>.json   { "<stashdb uuid>": ["url", ...] }
    site/data/avatars_jav/<shard>.json     { "<javstash uuid>": ["url", ...] }

分片：id 尾字符哈希 % 128（与前端一致），每片约 100~200 KB。

增量：结果缓存在 data/avatar_cache_<site>.json，重跑只拉新增的 id。

用法：
    set -a; . /root/.dsh/.env; set +a
    python3 scripts/build_avatar_index.py                 # 两站都拉
    python3 scripts/build_avatar_index.py --only stash --limit 500   # 调试
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SHARDS = 128
MAX_IMAGES = 6           # 每人最多存多少张，控制体积（6 张已够选）
WORKERS = 8
BATCH = 50               # 一次 GraphQL 查多少个演员（实测 100 也可以，50 更稳）
UA = "actor-em/1.0 (https://github.com/icezxf/actor-em)"

SITES = {
    "stash": {
        "db": "actors_eu.jsonl",
        "pid": "StashDB",
        "url": "https://stashdb.org/graphql",
        "env": "STASHDB_API_KEY",
        "outdir": "data/avatars_stash",
        "cache": "data/avatar_cache_stash.json",
        "field": "id",
    },
    "jav": {
        "db": "actors.jsonl",
        "pid": "JavStash",
        "url": "https://javstash.org/graphql",
        "env": "JAVSTASH_API_KEY",
        "outdir": "data/avatars_jav",
        "cache": "data/avatar_cache_jav.json",
        "field": "id",
    },
}

QUERY = "query($id: ID!) { findPerformer(id: $id) { name images { url } } }"


def shard_of(pid: str) -> int:
    """与前端一致：id 各字符码之和 % SHARDS。"""
    return sum(ord(c) for c in pid) % SHARDS


def fetch_batch(url: str, api_key: str, pids: list[str], retries: int = 3) -> dict[str, list[str]] | None:
    """一次 GraphQL 请求查一批演员（别名并行），返回 {pid: [urls]}。

    stash-box 的 findPerformer 每个查询只能查一个 ID，但 GraphQL 允许用别名
    在一个请求里并列多个查询。实测 100 个别名也没问题——原来一个人一次请求要
    7 万次调用，现在 50 个一批只要 1400 次，从 ~80 分钟降到几分钟。
    """
    parts, variables = [], {}
    for i, pid in enumerate(pids):
        parts.append(f"p{i}: findPerformer(id: $id{i}) {{ id images {{ url }} }}")
        variables[f"id{i}"] = pid
    decls = ",".join(f"$id{i}: ID!" for i in range(len(pids)))
    query = f"query({decls}) {{ " + " ".join(parts) + " }"
    body = json.dumps({"query": query, "variables": variables})

    for attempt in range(retries):
        try:
            raw = subprocess.run(
                ["curl", "-s", "--ipv4", "--max-time", "60", url,
                 "-H", "Content-Type: application/json",
                 "-H", f"ApiKey: {api_key}",
                 "-H", f"User-Agent: {UA}",
                 "--data-binary", body],
                capture_output=True, text=True, timeout=80,
            ).stdout
            if not raw.strip():
                time.sleep(1.5 * (attempt + 1))
                continue
            data = json.loads(raw)
            if data.get("errors"):
                time.sleep(1.5 * (attempt + 1))
                continue
            payload = data.get("data") or {}
            out: dict[str, list[str]] = {}
            for i, pid in enumerate(pids):
                node = payload.get(f"p{i}")
                out[pid] = [img["url"] for img in ((node or {}).get("images") or []) if img.get("url")]
            return out
        except Exception:
            time.sleep(1.5 * (attempt + 1))
    return None                    # 整批失败（不写缓存，下轮再试）


def load_ids(db_path: str, pid_field: str) -> list[str]:
    ids, seen = [], set()
    with open(db_path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                a = json.loads(line)
            except json.JSONDecodeError:
                continue
            pid = ((a.get("provider_ids") or {}).get(pid_field) or "").strip()
            if pid and pid not in seen:
                seen.add(pid)
                ids.append(pid)
    return ids


def run_site(name: str, cfg: dict, limit: int, no_fetch: bool = False) -> None:
    api_key = os.getenv(cfg["env"], "").strip()
    if not api_key and not no_fetch:
        print(f"[{name}] 跳过：缺少 {cfg['env']}", file=sys.stderr)
        return
    db_path = os.path.join(ROOT, cfg["db"])
    if not os.path.exists(db_path):
        print(f"[{name}] 跳过：找不到 {cfg['db']}", file=sys.stderr)
        return

    ids = load_ids(db_path, cfg["pid"])
    cache_path = os.path.join(ROOT, cfg["cache"])
    cache: dict[str, list[str]] = {}
    if os.path.exists(cache_path):
        try:
            with open(cache_path, encoding="utf-8") as f:
                cache = json.load(f)
        except Exception:
            cache = {}
    # 缓存文件不提交仓库（体积大）。从上次的分片把结果读回来，照样能增量。
    if not cache:
        outdir0 = os.path.join(ROOT, cfg["outdir"])
        if os.path.isdir(outdir0):
            for fn in os.listdir(outdir0):
                if not fn.endswith(".json"):
                    continue
                try:
                    with open(os.path.join(outdir0, fn), encoding="utf-8") as f:
                        cache.update(json.load(f))
                except Exception:
                    pass
            if cache:
                print(f"[{name}] 从上次分片恢复 {len(cache)} 条缓存", file=sys.stderr)
    todo = [] if no_fetch else [i for i in ids if i not in cache]
    if limit:
        todo = todo[:limit]
    print(f"[{name}] 库内 {len(ids)} 个 ID，缓存 {len(cache)}，本轮拉 {len(todo)}", file=sys.stderr)

    done = 0
    batches = [todo[i:i + BATCH] for i in range(0, len(todo), BATCH)]
    if batches:
        print(f"[{name}] 分 {len(batches)} 批（每批 {BATCH}）", file=sys.stderr)
        with ThreadPoolExecutor(max_workers=WORKERS) as ex:
            futures = {ex.submit(fetch_batch, cfg["url"], api_key, b): b for b in batches}
            for fut in as_completed(futures):
                batch = futures[fut]
                try:
                    got = fut.result()
                except Exception:
                    got = None
                if got is not None:
                    for pid, imgs in got.items():
                        cache[pid] = imgs[:MAX_IMAGES]
                done += len(batch)
                if done % 1000 < BATCH:
                    print(f"[{name}] {done}/{len(todo)}", file=sys.stderr)
                    # 中途落盘：跑到一半断网也不会全丢，下次从缓存继续
                    try:
                        os.makedirs(os.path.dirname(cache_path), exist_ok=True)
                        with open(cache_path, "w", encoding="utf-8") as cf:
                            json.dump(cache, cf, ensure_ascii=False, separators=(",", ":"))
                    except Exception:
                        pass

    os.makedirs(os.path.dirname(cache_path), exist_ok=True)
    with open(cache_path, "w", encoding="utf-8") as f:
        json.dump(cache, f, ensure_ascii=False, separators=(",", ":"))

    # 分片输出
    outdir = os.path.join(ROOT, cfg["outdir"])
    os.makedirs(outdir, exist_ok=True)
    buckets: list[dict[str, list[str]]] = [{} for _ in range(SHARDS)]
    total = 0
    for pid in ids:
        imgs = cache.get(pid) or []
        if imgs:
            buckets[shard_of(pid)][pid] = imgs
            total += len(imgs)
    size = 0
    for i, b in enumerate(buckets):
        p = os.path.join(outdir, f"{i}.json")
        with open(p, "w", encoding="utf-8") as f:
            json.dump(b, f, ensure_ascii=False, separators=(",", ":"))
        size += os.path.getsize(p)
    with_img = sum(1 for pid in ids if cache.get(pid))
    print(
        f"[{name}] {with_img}/{len(ids)} 人有图，{total} 张 URL → {outdir} ({size/1024/1024:.1f} MB)",
        file=sys.stderr,
    )


def main():
    ap = argparse.ArgumentParser(description="生成候选头像分片索引")
    ap.add_argument("--only", choices=["stash", "jav", "both"], default="both")
    ap.add_argument("--limit", type=int, default=0, help="每站最多新拉多少个（调试）")
    ap.add_argument("--no-fetch", action="store_true", help="只用缓存生成分片，不调 API（构建用，秒级）")
    args = ap.parse_args()
    sites = SITES if args.only == "both" else {args.only: SITES[args.only]}
    for name, cfg in sites.items():
        run_site(name, cfg, args.limit, args.no_fetch)


if __name__ == "__main__":
    main()
