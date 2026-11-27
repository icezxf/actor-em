#!/usr/bin/env python3
"""把人气排名文件写回演员数据库（方案 A），同时保留排名文件供页面查表（方案 B）。

逻辑：
  - data/popularity_jav.json   → actors.jsonl   按 provider_ids.JavStash 匹配
  - data/popularity_stash.json → actors_eu.jsonl 按 provider_ids.StashDB 匹配
  - 每条记录写 popularity_rank 字段（整数，1 开始；匹配不到就删掉旧值）
  - 同步派生 actors.json / actors_eu.json（下游用），保持与 jsonl 一致

排名文件是权威，本脚本只是把它投影进数据库快照。页面既可以读 popularity_rank，
也可以读排名文件；两边一致。

用法：
    python3 scripts/apply_popularity.py
    python3 scripts/apply_popularity.py --jp actors.jsonl --eu actors_eu.jsonl
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from datetime import datetime, timezone

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def load_ranks(path: str) -> dict[str, int]:
    if not os.path.exists(path):
        print(f"排名文件不存在：{path}（跳过这一站）", file=sys.stderr)
        return {}
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def apply_to_db(db_path: str, ranks: dict[str, int], site_key: str) -> tuple[int, int]:
    """返回 (写入条数, 清空条数)。"""
    if not ranks or not os.path.exists(db_path):
        return 0, 0
    rows = []
    with open(db_path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                try:
                    rows.append(json.loads(line))
                except json.JSONDecodeError:
                    pass
    written = cleared = 0
    for a in rows:
        pid = (a.get("provider_ids") or {}).get(site_key, "")
        rank = ranks.get(str(pid)) if pid else None
        old = a.get("popularity_rank")
        if rank is not None:
            if old != rank:
                written += 1
            a["popularity_rank"] = rank
        elif old is not None:
            a.pop("popularity_rank", None)
            cleared += 1
    rows.sort(key=lambda a: a["key"])
    with open(db_path, "w", encoding="utf-8") as f:
        for a in rows:
            f.write(json.dumps(a, ensure_ascii=False, separators=(",", ":")) + "\n")
    return written, cleared


def derive_json(jsonl_path: str, json_path: str):
    """从 jsonl 派生下游 json（与项目 ExportJSON 对齐：ok/partial + 无下划线）。"""
    if not os.path.exists(jsonl_path):
        return
    rows = []
    with open(jsonl_path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                try:
                    rows.append(json.loads(line))
                except json.JSONDecodeError:
                    pass
    exportable = [a for a in rows
                  if a.get("status") in ("ok", "partial") and "_" not in (a.get("name") or "")]
    now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    with open(json_path, "w", encoding="utf-8") as f:
        json.dump({"schema_version": 3, "generated_at": now,
                   "count": len(exportable), "actors": exportable},
                  f, ensure_ascii=False, separators=(",", ":"))
        f.write("\n")
    print(f"派生 {json_path}：{len(exportable)} 条", file=sys.stderr)


def main():
    ap = argparse.ArgumentParser(description="把人气排名写回演员数据库")
    ap.add_argument("--jp", default=os.path.join(ROOT, "actors.jsonl"))
    ap.add_argument("--eu", default=os.path.join(ROOT, "actors_eu.jsonl"))
    args = ap.parse_args()

    jav_ranks = load_ranks(os.path.join(ROOT, "data/popularity_jav.json"))
    stash_ranks = load_ranks(os.path.join(ROOT, "data/popularity_stash.json"))
    print(f"排名文件：jav {len(jav_ranks)} / stash {len(stash_ranks)}", file=sys.stderr)

    jp_w, jp_c = apply_to_db(args.jp, jav_ranks, "JavStash")
    print(f"日系库 {args.jp}：写入 {jp_w} / 清空 {jp_c}", file=sys.stderr)
    eu_w, eu_c = apply_to_db(args.eu, stash_ranks, "StashDB")
    print(f"欧美库 {args.eu}：写入 {eu_w} / 清空 {eu_c}", file=sys.stderr)

    derive_json(args.jp, args.jp[:-6] + ".json" if args.jp.endswith(".jsonl") else args.jp + ".json")
    derive_json(args.eu, args.eu[:-6] + ".json" if args.eu.endswith(".jsonl") else args.eu + ".json")


if __name__ == "__main__":
    main()
