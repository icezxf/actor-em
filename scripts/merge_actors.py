#!/usr/bin/env python3
"""合并日系库 actors.jsonl + 欧美库 actors_eu.jsonl → actors_all.jsonl / actors_all.json。

规则（用户指定：冲突以日系库为准）：
  - 按 key 并集去重；同 key 时以日系记录为底
  - 标量字段：日系有值保留，空位才用欧美填
  - 数组 aliases/tags：并集去重（日系在前）
  - map provider_ids/sources：并集，日系值优先
  - bio：日系有就留，只有空位才用欧美；bio_lang 跟着 bio 走
  - key / name / status / created_at / fail_count / retry_after 等控制字段：完全用日系

产物：actors_all.jsonl（真源）、actors_all.json（下游用）、actors_all.manifest.json（健康度）
不改动任何原库文件。

用法：
    python3 scripts/merge_actors.py
    python3 scripts/merge_actors.py --jp actors.jsonl --eu actors_eu.jsonl --out actors_all
"""

from __future__ import annotations

import argparse
import json
import os
from datetime import datetime, timezone

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# 有值即算占用（空串/None/0/空数组/空 map 都算空）
def filled(v):
    if v is None:
        return False
    if isinstance(v, str):
        return v.strip() != ""
    if isinstance(v, (int, float)):
        return v != 0
    if isinstance(v, (list, dict)):
        return len(v) > 0
    return True

SCALARS = [
    "name_original", "birth_date", "birth_year", "birth_date_source",
    "birth_place", "country", "height_cm", "weight_kg", "measurements",
    "measurements_unit", "cup_size", "career_start", "bio", "bio_lang",
    "avatar", "avatar_cn", "avatar_source_site", "homepage_url",
]

def load(path):
    rows = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                a = json.loads(line)
            except json.JSONDecodeError:
                continue
            if a.get("key"):
                rows.append(a)
    return rows

def merge_pair(jp: dict, eu: dict) -> dict:
    """以日系为底，欧美填空位。"""
    out = dict(jp)  # 控制字段（key/name/status/created_at/…）完全来自日系
    for k in SCALARS:
        if not filled(out.get(k)) and filled(eu.get(k)):
            out[k] = eu[k]
    # bio 和 bio_lang 必须成对：日系 bio 空而用了欧美 bio 时，lang 也跟着换
    if not filled(jp.get("bio")) and filled(eu.get("bio")):
        out["bio"] = eu["bio"]
        out["bio_lang"] = eu.get("bio_lang")
    # 数组并集（日系在前）
    for k in ("aliases", "tags"):
        merged = list(out.get(k) or [])
        seen = {str(x) for x in merged}
        for x in (eu.get(k) or []):
            s = str(x)
            if s not in seen:
                seen.add(s)
                merged.append(x)
        out[k] = merged
    # map 并集，日系优先
    for k in ("provider_ids", "sources"):
        m = dict(out.get(k) or {})
        for sk, sv in (eu.get(k) or {}).items():
            if sk not in m or not filled(m[sk]):
                m[sk] = sv
        out[k] = m
    return out

def main():
    ap = argparse.ArgumentParser(description="合并日系+欧美演员库")
    ap.add_argument("--jp", default=os.path.join(ROOT, "actors.jsonl"))
    ap.add_argument("--eu", default=os.path.join(ROOT, "actors_eu.jsonl"))
    ap.add_argument("--out", default="actors_all", help="输出文件前缀")
    args = ap.parse_args()

    jp_rows = load(args.jp)
    eu_rows = load(args.eu)
    print(f"日系 {len(jp_rows)} 条 / 欧美 {len(eu_rows)} 条")

    jp = {a["key"]: a for a in jp_rows}
    eu = {a["key"]: a for a in eu_rows}
    overlap = set(jp) & set(eu)
    print(f"主键重叠 {len(overlap)} 条（以日系为准，欧美填空位）")

    merged = dict(jp)
    for k, e in eu.items():
        if k in jp:
            merged[k] = merge_pair(jp[k], e)
        else:
            merged[k] = e

    out_rows = sorted(merged.values(), key=lambda a: a["key"])
    jsonl_path = os.path.join(ROOT, args.out + ".jsonl")
    with open(jsonl_path, "w", encoding="utf-8") as f:
        for a in out_rows:
            f.write(json.dumps(a, ensure_ascii=False, separators=(",", ":")) + "\n")

    # 下游 actors.json：只含 ok/partial 且名字无下划线（与 Go Exportable 对齐）
    exportable = [a for a in out_rows
                  if a.get("status") in ("ok", "partial") and "_" not in (a.get("name") or "")]
    now = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    json_path = os.path.join(ROOT, args.out + ".json")
    with open(json_path, "w", encoding="utf-8") as f:
        json.dump({"schema_version": 3, "generated_at": now,
                   "count": len(exportable), "actors": exportable},
                  f, ensure_ascii=False, indent=2)
        f.write("\n")

    from collections import Counter
    manifest = {
        "schema_version": 3, "generated_at": now, "count": len(out_rows),
        "by_status": dict(Counter(a.get("status", "") for a in out_rows)),
        "sources": {"jp": len(jp_rows), "eu": len(eu_rows), "overlap": len(overlap)},
    }
    mpath = os.path.join(ROOT, args.out + ".manifest.json")
    with open(mpath, "w", encoding="utf-8") as f:
        json.dump(manifest, f, ensure_ascii=False, indent=2)
        f.write("\n")

    print(f"合并完成：{len(out_rows)} 条（导出 {len(exportable)} 条）")
    print(f"  {jsonl_path}")
    print(f"  {json_path}")
    print(f"  {mpath}")

if __name__ == "__main__":
    main()
