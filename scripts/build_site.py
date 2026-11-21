#!/usr/bin/env python3
"""把 actors.json / actors.jsonl 派生出一个可搜索的静态站（GitHub Pages 用）。

产物结构：
    site/index.html
    site/assets/app.js, style.css
    site/data/actors.json      # 精简后的演员索引，前端一次拉完
    site/data/meta.json        # 数量、生成时间、数据版本

设计取舍：
  - 只保留 ok/partial 且名字里没有下划线的记录，和 Go 侧 Exportable() 对齐；
  - 字段压成定长数组（不是对象），16k 条能省掉约 40% 体积；
  - 头像 URL 只存域名缩写（~g/ ~t/ ~j/ ~s/），前端再展开；
  - 简介只有 9% 的记录有，直接内联，不做二次请求。
"""

from __future__ import annotations

import json
import os
import shutil
import sys
from datetime import datetime, timezone

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT_DIR = os.path.join(ROOT, "site")
SRC_DIR = os.path.join(ROOT, "site_src")

# 头像域名缩写：{前缀: 代号}，前端按同表展开
AVATAR_PREFIXES = [
    ("https://cdn.jsdelivr.net/gh/gfriends/gfriends@master/Content/", "~g/"),
    ("https://cdn.theporndb.net/", "~t/"),
    ("https://javstash.org/", "~j/"),
    ("https://stashdb.org/", "~s/"),
]

# 行字段顺序，前端 app.js 里按同样顺序解包
FIELDS = [
    "id", "name", "name_original", "avatar", "birth_date", "height_cm",
    "cup_size", "measurements", "career_start", "tags", "aliases", "birth_place",
]

# 别名给搜索用，个别人有上百条（含各种活动名），全带上会把索引撑大 0.5MB，取前 12 条足够。
MAX_ALIASES = 12

# 简介和主页链接只在点开详情时用得到，拆成 16 个分片按需加载：
# 首屏索引从 5.5MB 降到 3.1MB（gzip ~1.1MB），点一个人只多拉一个 ~130KB 的分片。
DETAIL_SHARDS = 16


def load_rows(path: str) -> list[dict]:
    if path.endswith(".jsonl"):
        rows = []
        with open(path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if line:
                    rows.append(json.loads(line))
        return rows
    with open(path, encoding="utf-8") as f:
        data = json.load(f)
    if isinstance(data, dict):
        return data.get("actors") or []
    return data


def exportable(a: dict) -> bool:
    """和 Go 侧 Actor.Exportable() 对齐：只导出 ok/partial，且排除带下划线的旧名。"""
    if a.get("status") not in ("ok", "partial"):
        return False
    name = a.get("name") or ""
    if not name or "_" in name:
        return False
    return True


def compact_avatar(url: str) -> str:
    url = (url or "").strip()
    if not url:
        return ""
    for prefix, code in AVATAR_PREFIXES:
        if url.startswith(prefix):
            return code + url[len(prefix):]
    return url


def build_rows(rows: list[dict]):
    """返回 (索引行, 详情分片)。详情分片只存 [简介, 主页]，按 id 取模分桶。"""
    out = []
    details: list[dict] = [{} for _ in range(DETAIL_SHARDS)]
    for a in rows:
        if not exportable(a):
            continue
        idx = len(out)
        avatar = compact_avatar(a.get("avatar_cn") or a.get("avatar") or "")
        out.append([
            idx,                                        # 0 id
            a.get("name") or "",                        # 1
            a.get("name_original") or "",               # 2
            avatar,                                     # 3
            a.get("birth_date") or "",                  # 4
            a.get("height_cm") or 0,                    # 5
            a.get("cup_size") or "",                    # 6
            a.get("measurements") or "",                # 7
            a.get("career_start") or 0,                 # 8
            "|".join(a.get("tags") or []),              # 9
            "|".join((a.get("aliases") or [])[:MAX_ALIASES]),  # 10
            a.get("birth_place") or "",                 # 11
        ])
        bio = a.get("bio") or ""
        home = a.get("homepage_url") or ""
        if bio or home:
            details[idx % DETAIL_SHARDS][str(idx)] = [bio, home]
    return out, details


def main() -> int:
    src = sys.argv[1] if len(sys.argv) > 1 else os.path.join(ROOT, "actors.json")
    if not os.path.exists(src):
        print(f"找不到数据文件：{src}", file=sys.stderr)
        return 1

    rows = load_rows(src)
    data_rows, details = build_rows(rows)

    # 原地覆盖，不删目录：产物是固定文件名，旧文件会被同名覆盖，
    # 分片数量也是固定的 16 个，不需要清理。
    os.makedirs(os.path.join(OUT_DIR, "data", "detail"), exist_ok=True)
    os.makedirs(os.path.join(OUT_DIR, "assets"), exist_ok=True)

    for name in ("index.html",):
        shutil.copyfile(os.path.join(SRC_DIR, name), os.path.join(OUT_DIR, name))
    for name in ("app.js", "style.css"):
        shutil.copyfile(os.path.join(SRC_DIR, "assets", name),
                        os.path.join(OUT_DIR, "assets", name))

    payload = {"fields": FIELDS, "rows": data_rows}
    data_path = os.path.join(OUT_DIR, "data", "actors.json")
    with open(data_path, "w", encoding="utf-8") as f:
        json.dump(payload, f, ensure_ascii=False, separators=(",", ":"))

    detail_bytes = 0
    for i, bucket in enumerate(details):
        p = os.path.join(OUT_DIR, "data", "detail", f"{i}.json")
        with open(p, "w", encoding="utf-8") as f:
            json.dump(bucket, f, ensure_ascii=False, separators=(",", ":"))
        detail_bytes += os.path.getsize(p)

    meta = {
        "count": len(data_rows),
        "generated_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "source": os.path.basename(src),
    }
    with open(os.path.join(OUT_DIR, "data", "meta.json"), "w", encoding="utf-8") as f:
        json.dump(meta, f, ensure_ascii=False, indent=2)

    size = os.path.getsize(data_path)
    print(f"演员 {len(data_rows)} 条（源 {len(rows)} 条）→ {data_path} ({size/1024/1024:.2f} MB)")
    print(f"详情分片 {DETAIL_SHARDS} 个，共 {detail_bytes/1024/1024:.2f} MB")
    print(f"站点输出：{OUT_DIR}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
