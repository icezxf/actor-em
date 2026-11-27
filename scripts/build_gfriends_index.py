#!/usr/bin/env python3
"""从 gfriends 图库索引生成「按名字分片」的候选图索引，供静态站按需查询。

背景：
  gfriends 的 Filetree.json 有 6.3MB，前端直接加载太重。但它就是
  「类别 → {key: value}」的目录树，一个演员可能在多个类别里各有一张图。
  我们按演员名做哈希分片，每片几十 KB，详情页点开某个人时只拉那一片。

输出：
    site/data/gfriends/<shard>.json   { "演员名": ["url1", "url2", ...], ... }
    分片号 = 名字哈希 % SHARDS，前端用同一算法算 shard

注意索引里 key→value 的配对不可信（实测 36.8% 对不上），
所以只用 value 集合（「这个目录里存在哪些文件」可信），文件名即演员名。

用法：
    python3 scripts/build_gfriends_index.py            # 下载官方索引
    python3 scripts/build_gfriends_index.py --local /tmp/ft.json
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT_DIR = os.path.join(ROOT, "site", "data", "gfriends")
SHARDS = 128
BASE = "https://raw.githubusercontent.com/gfriends/gfriends/master/Content/"

INDEX_URLS = [
    "https://raw.githubusercontent.com/gfriends/gfriends/master/Filetree.json",
    "https://cdn.jsdelivr.net/gh/gfriends/gfriends@master/Filetree.json",
]

IMG_EXT = (".jpg", ".jpeg", ".png", ".webp")

# 目录优先级（与 gfriends.go 一致）：靠前的优先展示
DIR_ORDER = [
    "0-Hand-Storage", "y-Minnano", "z-DMM(骑)", "z-DMM(步)", "y-AVDC",
    "z-Derekhsu", "y-无垢", "z-ラグジュTV", "9-AVDBS", "7-Moodyz",
    "7-Madonna", "8-Ideapocket", "v-Attackers", "9-Javrave", "8-Warashi",
]


def shard_of(name: str) -> int:
    """分片号 = 字符码之和 % SHARDS。

    故意用最简单、可预测的算法：前端 app.js 里 gfriendsShard() 必须算出同一个值，
    复杂的 32 位溢出语义在 JS/Python 之间容易出错（中文都在 BMP，charCodeAt 即码点）。
    """
    return sum(ord(c) for c in name) % SHARDS


def fetch_index(local: str | None) -> dict:
    if local:
        with open(local, encoding="utf-8") as f:
            return json.load(f)
    for url in INDEX_URLS:
        try:
            # curl + ipv4：raw.githubusercontent 的 IPv6 路由常被重置
            raw = subprocess.run(
                ["curl", "-s", "--ipv4", "--max-time", "120", url],
                capture_output=True, text=True, timeout=150,
            ).stdout
            if raw and raw.strip().startswith("{"):
                return json.loads(raw)
            print(f"索引下载为空：{url}", file=sys.stderr)
        except Exception as e:
            print(f"索引下载失败 {url}: {e}", file=sys.stderr)
    raise SystemExit("拿不到 gfriends 索引")


def main():
    ap = argparse.ArgumentParser(description="生成 gfriends 分片候选图索引")
    ap.add_argument("--local", help="本地 Filetree.json（免下载）")
    args = ap.parse_args()

    tree = fetch_index(args.local)
    content = tree.get("Content") or {}
    print(f"索引类别 {len(content)} 个", file=sys.stderr)

    # 名字 → [(类别, 文件名)]；只用 value 集合，不用坏掉的 key
    by_name: dict[str, list[tuple[str, str]]] = {}
    for cat, files in content.items():
        for v in set(files.values()):
            fname = v.split("?")[0]
            if not fname.lower().endswith(IMG_EXT):
                continue
            stem = fname[len("AI-Fix-"):] if fname.startswith("AI-Fix-") else fname
            name = stem.rsplit(".", 1)[0].strip()
            if not name:
                continue
            by_name.setdefault(name, []).append((cat, fname))

    def dir_rank(cat: str) -> int:
        try:
            return DIR_ORDER.index(cat)
        except ValueError:
            return len(DIR_ORDER)

    shards: list[dict[str, list[str]]] = [{} for _ in range(SHARDS)]
    for name, items in by_name.items():
        # 按目录优先级排；同目录内原图优先于 AI-Fix
        items.sort(key=lambda it: (dir_rank(it[0]), 0 if not it[1].startswith("AI-Fix-") else 1, it[1]))
        urls = [f"{BASE}{cat}/{fname}" for cat, fname in items]
        # 去重保序
        seen = set()
        urls = [u for u in urls if not (u in seen or seen.add(u))]
        shards[shard_of(name)][name] = urls

    os.makedirs(OUT_DIR, exist_ok=True)
    total_bytes = 0
    for i, bucket in enumerate(shards):
        p = os.path.join(OUT_DIR, f"{i}.json")
        with open(p, "w", encoding="utf-8") as f:
            json.dump(bucket, f, ensure_ascii=False, separators=(",", ":"))
        total_bytes += os.path.getsize(p)

    multi = sum(1 for v in by_name.values() if len(v) > 1)
    print(
        f"演员 {len(by_name)} 人（{multi} 人有 2+ 张图）→ {SHARDS} 个分片，"
        f"共 {total_bytes/1024/1024:.1f} MB",
        file=sys.stderr,
    )


if __name__ == "__main__":
    main()
