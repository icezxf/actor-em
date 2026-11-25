#!/usr/bin/env python3
"""把 actors.json / actors.jsonl 派生出一个可搜索的静态站（GitHub Pages 用）。

产物结构：
    site/index.html
    site/assets/app.js, tables.js, style.css
    site/data/actors.json      # 精简后的演员索引，前端一次拉完
    site/data/dict.json        # 合并后的数据字典（actor_dict.csv，按人归并别名）
    site/data/mdcx.json        # MDCx 原始档案（mdcx_profiles.csv），简介另存分片
    site/data/mdcx_bio/<n>.json
    site/data/meta.json        # 数量、生成时间、数据版本

设计取舍：
  - 只保留 ok/partial 且名字里没有下划线的记录，和 Go 侧 Exportable() 对齐；
  - 字段压成定长数组（不是对象），16k 条能省掉约 40% 体积；
  - 头像 URL 只存域名缩写（~g/ ~t/ ~j/ ~s/），前端再展开；
  - 简介和主页在分片里，点开才拉；
  - 字典按「日文名+中文名+来源」归并，68k 行压到 23k 行（别名挤在一格里）；
  - MDCx 表同名字段只留一份，bio 拆到分片，表格页只拉结构字段。
"""

from __future__ import annotations

import csv
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
# src 标记来源：jp=日系（actors.jsonl）/ eu=欧美（actors_eu.jsonl）
FIELDS = [
    "id", "name", "name_original", "avatar", "birth_date", "height_cm",
    "cup_size", "measurements", "career_start", "tags", "aliases", "birth_place",
    # country 是 ISO 码（JP/US/TW…），前端翻成中文并做「日本 / 其他 → 具体国家」两级筛选
    "country", "src",
]

# 别名给搜索用，个别人有上百条（含各种活动名），全带上会把索引撑大 0.5MB，取前 12 条足够。
MAX_ALIASES = 12

# 简介和主页链接只在点开详情时用得到，拆成 16 个分片按需加载：
# 首屏索引从 5.5MB 降到 3.1MB（gzip ~1.1MB），点一个人只多拉一个 ~130KB 的分片。
DETAIL_SHARDS = 16

# 合并字典的列（actor_dict.csv 是「别名, 日文名, 中文名, 来源」，按人归并后只留这一组）
DICT_FIELDS = ["ja", "zh", "source", "aliases"]

# MDCx 档案表的列；bio 太长，单独拆分片
MDCX_FIELDS = [
    "name", "name_cn", "romaji", "aliases", "birthday", "birthplace", "country",
    "height", "cup", "bust", "waist", "hip", "career_start", "tags",
]
MDCX_BIO_SHARDS = 8


def build_dict(path: str):
    """actor_dict.csv → [[日文名, 中文名, 来源, "别名1|别名2"]...]

    CSV 是「一个别名一行」（68k 行），同一个人的多个别名重复了日文名/中文名。
    按 (日文名, 中文名, 来源) 归并后是 23k 行，前端一行一条更符合人的阅读习惯。
    """
    if not os.path.exists(path):
        return None
    groups: dict[tuple, list[str]] = {}
    order: list[tuple] = []
    with open(path, encoding="utf-8-sig", newline="") as f:
        for r in csv.DictReader(f):
            ja = (r.get("日文名") or "").strip()
            zh = (r.get("中文名") or "").strip()
            src = (r.get("来源") or "").strip()
            alias = (r.get("别名") or "").strip()
            if not ja and not zh:
                continue
            key = (ja, zh, src)
            if key not in groups:
                groups[key] = []
                order.append(key)
            if alias and alias not in groups[key]:
                groups[key].append(alias)
    rows = [[ja, zh, src, "|".join(groups[(ja, zh, src)])] for ja, zh, src in order]
    return rows


def build_mdcx(path: str):
    """mdcx_profiles.csv → (结构字段行, 简介分片)。"""
    if not os.path.exists(path):
        return None, None
    bios: list[dict] = [{} for _ in range(MDCX_BIO_SHARDS)]
    rows = []
    with open(path, encoding="utf-8-sig", newline="") as f:
        for r in csv.DictReader(f):
            name = (r.get("name") or "").strip()
            if not name:
                continue
            idx = len(rows)
            rows.append([
                name,                                   # 0 日文名
                (r.get("name_cn") or "").strip(),       # 1 中文名
                (r.get("romaji") or "").strip(),        # 2 罗马字
                (r.get("aliases") or "").strip(),       # 3 别名
                (r.get("birthday") or "").strip(),      # 4 生日
                (r.get("birthplace") or "").strip(),    # 5 出生地
                (r.get("country") or "").strip(),       # 6 国家
                _int(r.get("height")),                  # 7 身高
                (r.get("cup") or "").strip(),           # 8 罩杯
                _int(r.get("bust")),                    # 9 胸围(cm)
                _int(r.get("waist")),                   # 10 腰围(cm)
                _int(r.get("hip")),                     # 11 臀围(cm)
                _int(r.get("career_start")),            # 12 出道年
                (r.get("tags") or "").strip(),          # 13 标签
            ])
            bio = (r.get("bio") or "").strip()
            if bio:
                bios[idx % MDCX_BIO_SHARDS][str(idx)] = bio
    return rows, bios


def _int(v):
    v = (v or "").strip()
    if not v:
        return 0
    try:
        return int(float(v))
    except ValueError:
        return 0


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


def build_rows(rows: list[dict], src: str = "jp"):
    """返回 (索引行, 详情分片)。详情分片只存 [简介, 主页]，按 id 取模分桶。

    src: jp / eu，写进每行末列供前端筛选。两批数据合并时 id 要错开，
    否则详情分片按 id 取模会撞——用 (src 前缀 + 序号) 不行（前端按数字取模），
    所以改成全局递增的连续 id（由调用方保证两批 id 不重叠）。
    """
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
            a.get("country") or "",                     # 12
            src,                                        # 13 来源 jp/eu
        ])
        bio = a.get("bio") or ""
        home = a.get("homepage_url") or ""
        # 外部链接按 javstash → stashdb → tpdb 排（provider_ids 里存的站点 ID）
        links = []
        pids = a.get("provider_ids") or {}
        for site, tmpl in (
            ("JavStash", "https://javstash.org/performers/{}"),
            ("StashDB", "https://stashdb.org/performers/{}"),
            ("TPDB", "https://theporndb.net/performers/{}"),
        ):
            pid = (pids.get(site) or "").strip()
            if pid:
                links.append([site, tmpl.format(pid)])
        if bio or home or links:
            details[idx % DETAIL_SHARDS][str(idx)] = [bio, home, links]
    return out, details


def main() -> int:
    src = sys.argv[1] if len(sys.argv) > 1 else os.path.join(ROOT, "actors.json")
    if not os.path.exists(src):
        print(f"找不到数据文件：{src}", file=sys.stderr)
        return 1

    rows = load_rows(src)
    data_rows, details = build_rows(rows, src="jp")

    # 欧美库：存在就并进同一份索引，标 src=eu，前端可单独筛选。
    # id 必须全局连续（build_rows 内部按 len(out) 生成），所以先记录当前数量，
    # 欧洲批次的 id 偏移由合并函数统一处理。
    eu_path = os.path.join(ROOT, "actors_eu.jsonl")
    eu_count = 0
    if os.path.exists(eu_path):
        eu_rows = load_rows(eu_path)
        eu_data, eu_details = build_rows(eu_rows, src="eu")
        offset = len(data_rows)
        for r in eu_data:
            r[0] = r[0] + offset                 # id 全局递增，避免分片 key 撞车
        data_rows.extend(eu_data)
        # 详情必须按"全局 id % 16"重新分桶，前端 loadDetail 就是这么找的。
        # build_rows 里欧美的桶是按局部 idx 分的，offset>0 时会错位，这里重排。
        for i, bucket in enumerate(eu_details):
            for k, v in bucket.items():
                gid = int(k) + offset
                details[gid % DETAIL_SHARDS][str(gid)] = v
        eu_count = len(eu_data)
        print(f"欧美库 {eu_count} 条并入（源 {len(eu_rows)} 条）→ {eu_path}")
    else:
        print(f"跳过欧美库：找不到 {eu_path}", file=sys.stderr)

    # 原地覆盖，不删目录：产物是固定文件名，旧文件会被同名覆盖，
    # 分片数量也是固定的 16 个，不需要清理。
    os.makedirs(os.path.join(OUT_DIR, "data", "detail"), exist_ok=True)
    os.makedirs(os.path.join(OUT_DIR, "assets"), exist_ok=True)

    for name in ("index.html",):
        shutil.copyfile(os.path.join(SRC_DIR, name), os.path.join(OUT_DIR, name))
    for name in ("app.js", "tables.js", "style.css"):
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

    # 字典与 MDCx 表：页面里用来跟最终记录对照，缺文件就跳过（不挂页面）。
    dict_path = os.path.join(ROOT, "actor_dict.csv")
    dict_rows = build_dict(dict_path)
    if dict_rows is not None:
        p = os.path.join(OUT_DIR, "data", "dict.json")
        with open(p, "w", encoding="utf-8") as f:
            json.dump({"fields": DICT_FIELDS, "rows": dict_rows}, f,
                      ensure_ascii=False, separators=(",", ":"))
        meta["dict"] = {"count": len(dict_rows), "bytes": os.path.getsize(p)}
        print(f"字典 {len(dict_rows)} 行 → {p} ({os.path.getsize(p)/1024/1024:.2f} MB)")
    else:
        print(f"跳过字典：找不到 {dict_path}", file=sys.stderr)

    mdcx_rows, mdcx_bios = build_mdcx(os.path.join(ROOT, "mdcx_profiles.csv"))
    if mdcx_rows:
        p = os.path.join(OUT_DIR, "data", "mdcx.json")
        with open(p, "w", encoding="utf-8") as f:
            json.dump({"fields": MDCX_FIELDS, "rows": mdcx_rows}, f,
                      ensure_ascii=False, separators=(",", ":"))
        os.makedirs(os.path.join(OUT_DIR, "data", "mdcx_bio"), exist_ok=True)
        bio_bytes = 0
        for i, bucket in enumerate(mdcx_bios or []):
            bp = os.path.join(OUT_DIR, "data", "mdcx_bio", f"{i}.json")
            with open(bp, "w", encoding="utf-8") as f:
                json.dump(bucket, f, ensure_ascii=False, separators=(",", ":"))
            bio_bytes += os.path.getsize(bp)
        meta["mdcx"] = {"count": len(mdcx_rows), "bytes": os.path.getsize(p),
                        "bio_bytes": bio_bytes}
        print(f"MDCx {len(mdcx_rows)} 行 → {p} ({os.path.getsize(p)/1024/1024:.2f} MB)，"
              f"简介分片 {len(mdcx_bios or [])} 个共 {bio_bytes/1024/1024:.2f} MB")
    else:
        print("跳过 MDCx：找不到 mdcx_profiles.csv", file=sys.stderr)

    with open(os.path.join(OUT_DIR, "data", "meta.json"), "w", encoding="utf-8") as f:
        json.dump(meta, f, ensure_ascii=False, indent=2)

    size = os.path.getsize(data_path)
    print(f"演员 {len(data_rows)} 条（源 {len(rows)} 条）→ {data_path} ({size/1024/1024:.2f} MB)")
    print(f"详情分片 {DETAIL_SHARDS} 个，共 {detail_bytes/1024/1024:.2f} MB")
    print(f"站点输出：{OUT_DIR}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
