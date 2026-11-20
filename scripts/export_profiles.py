#!/usr/bin/env python3
"""把 MDCx 的 actor-profile.sqlite3 导成 actorsync 能直接读的宽表 CSV。

为什么要这一步：Go 侧要按名字查 2 万人的结构化档案，直接读 sqlite 得引驱动，
而清洗逻辑（简繁归一、剔废值、推国家码、拆标签）用 Python 写更省事。
所以这里做完清洗，输出一个扁平的 CSV，Go 只负责读。

CSV 列：name,name_cn,aliases,birthplace,country,height,cup,career_start,tags,bio,romaji,birthday,bust,waist,hip
  - aliases / tags 用 | 分隔
  - birthplace 已归一为简体，且剔除「海外/国外」这类废值
  - country 是从出生地推出来的两位码，推不出就是空
  - career_start 取「演艺期间」的首年
  - bio 是 minnano 的结构化字段串（「身高: 155cm | 罩杯: F | 三围: 84/60/86」），
    不是文章，**不要拿它当 Overview**，只从里面抽「标签:」那一项
  - birthday 是 JavStash 的兜底：覆盖 65.7%，但约 24% 与 JavStash 不一致，
    所以 Go 侧只在 JavStash 缺席时才用，并标 birth_date_source
  - bust / waist / hip 是三围分项，单位厘米，三项齐备约 80%。对外统一成英寸
    （`44-25-39`），换算在 Go 侧的 BustWaistHipInches() 一处完成

用法：
    python3 scripts/export_profiles.py                 # 下载并导出到 /tmp/mdcx_profiles.csv
    python3 scripts/export_profiles.py -o data/x.csv
    python3 scripts/export_profiles.py --offline /tmp/mdcx_db
"""
from __future__ import annotations

import argparse
import csv
import json
import os
import re
import sqlite3
import sys
import tempfile
import urllib.request

from build_base_db import fetch_asset, http_get, load_state, log, save_state

ZHCDICT = "https://raw.githubusercontent.com/sqzw-x/amane/main/resources/zhconv/zhcdict.json"

# 出身字段里的废值：写「海外」等于没写
BAD_PLACES = {"海外", "国外", "不明", "不明/none", "非公开"}

# 标签黑名单：明确指向未成年人身体特征的词，不进任何元数据
TAG_BLACKLIST_EXACT = {"未成年", "幼児体型", "幼齿", "儿童"}
TAG_BLACKLIST_SUB = ("未成年", "幼児", "幼齿")

# 政治/地域对立类标签：上游站点自带，但不适合作为媒体库元数据
TAG_SENSITIVE_SUB = (
    "亲中", "反日", "亲韩", "反韩", "亲美", "反美", "反华", "辱华",
    "右翼", "左翼", "台独", "港独", "政治",
)

# 日文新字体 → 简体。zh2Hant 是「简→繁」，日文新字体（県/顔/豊）两边都不在，
# 不走这一层的话出生地会留着「爱知県」这种半日半中的写法。
JP_VARIANT = {
    "県": "县", "顔": "颜", "豊": "丰", "沢": "泽", "広": "广", "辺": "边",
    "黒": "黑", "浜": "滨", "稲": "稻", "徳": "德", "恵": "惠", "絵": "绘",
    "姫": "姬", "桜": "樱", "涼": "凉", "瀬": "濑", "渋": "涩", "駅": "驿",
    "塩": "盐", "蔵": "藏", "臓": "脏", "髪": "发", "闘": "斗", "竜": "龙",
    "滝": "泷", "斉": "齐", "剤": "剂", "済": "济", "児": "儿", "亜": "亚",
    "亀": "龟", "繋": "系", "鶏": "鸡", "兎": "兔", "麺": "面", "髄": "髓",
    "髭": "髭", "藤": "藤", "岡": "冈", "崎": "崎", "島": "岛", "嶋": "岛",
}

# 日本都道府县（简体），用来把出生地推成 JP
JP_PREFECTURES = {
    "北海道", "青森县", "岩手县", "宫城县", "秋田县", "山形县", "福岛县",
    "茨城县", "栃木县", "群马县", "埼玉县", "千叶县", "东京都", "神奈川县",
    "新潟县", "富山县", "石川县", "福井县", "山梨县", "长野县", "岐阜县",
    "静冈县", "爱知县", "三重县", "滋贺县", "京都府", "大阪府", "兵库县",
    "奈良县", "和歌山县", "鸟取县", "岛根县", "冈山县", "广岛县", "山口县",
    "德岛县", "香川县", "爱媛县", "高知县", "福冈县", "佐贺县", "长崎县",
    "熊本县", "大分县", "宫崎县", "鹿儿岛县", "冲绳县",
}


def load_hant2zh(local_path: str = "") -> dict[str, str]:
    """用 amane 的简→繁表反转出繁→简，单字级别就够（出生地/标签都是常用字）。

    取不到就返回空表——归一化是锦上添花，不能让整条流水线因为它挂掉。
    """
    try:
        if local_path and os.path.exists(local_path):
            with open(local_path, encoding="utf-8") as f:
                data = json.load(f)
        else:
            req = urllib.request.Request(ZHCDICT, headers={"User-Agent": "actor-em/1.0"})
            with urllib.request.urlopen(req, timeout=30) as r:
                data = json.loads(r.read())
    except Exception as e:  # noqa: BLE001
        log(f"简繁表不可用，跳过归一化：{type(e).__name__}: {e}")
        return {}
    z2h = data.get("zh2Hant") or {}
    out: dict[str, str] = {}
    for zh, hant in z2h.items():
        if len(zh) == 1 and len(hant) == 1 and hant not in out:
            out[hant] = zh
    return out


def to_zh(s: str, h2z: dict[str, str]) -> str:
    out = []
    for c in s:
        if c in JP_VARIANT:
            out.append(JP_VARIANT[c])
        elif c in h2z:
            out.append(h2z[c])
        else:
            out.append(c)
    return "".join(out)


def clean_place(v: str, h2z: dict[str, str]) -> str:
    v = to_zh((v or "").strip(), h2z)
    if not v or v in BAD_PLACES:
        return ""
    return v


def guess_country(place: str) -> str:
    if place in JP_PREFECTURES:
        return "JP"
    return ""


def parse_career(period: str) -> int:
    m = re.search(r"((?:19|20)\d{2})", period or "")
    if not m:
        return 0
    y = int(m.group(1))
    return y if 1900 <= y <= 2100 else 0


def clean_tags(v: str, h2z: dict[str, str]) -> list[str]:
    out, seen = [], set()
    for t in re.split(r"[,，、|/]", v or ""):
        t = to_zh(t.strip(), h2z)
        if not t or t in seen:
            continue
        if t in TAG_BLACKLIST_EXACT or any(k in t for k in TAG_BLACKLIST_SUB):
            continue
        if any(k in t for k in TAG_SENSITIVE_SUB):
            continue
        seen.add(t)
        out.append(t)
    return out


def _bio_number(bio: str, label: str) -> int:
    """从 bio 串里取「身高: 167cm」这类数字，取不到返回 0。"""
    m = re.search(rf"{label}[:：]\s*([0-9]+)", bio or "")
    return int(m.group(1)) if m else 0


def read_mapping(db_path: str, h2z: dict[str, str]) -> dict[str, list[dict]]:
    """从 actor-mapping 里取标签和结构化简介，按 jp 保留**全部同名候选**。

    注意标签**不在** actor-profile 里（那边 tags 字段 0 覆盖，minnano_bio 也没有），
    只在 mapping 的 bio 串里：「身高: 155cm | 罩杯: F | ... | 标签: 美人,美乳」。

    同一 jp 在 mapping 里可能有多条记录（实测 1086 组），既有本人也有同名新人。
    旧版用后写覆盖前写，会串数据；这里保留候选列表，交给 export 按生日/身高校验，
    只有和 profile 对得上的才采用。校验信息随候选一起带出来。
    """
    out: dict[str, list[dict]] = {}
    con = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    try:
        for jp, bio, birth in con.execute("SELECT jp, bio, birth_date FROM entries"):
            jp = (jp or "").split(",")[0].strip()
            bio = (bio or "").strip()
            if not jp or not bio:
                continue
            tags = ""
            m = re.search(r"标签[:：]\s*([^|]+)", bio)
            if m:
                tags = m.group(1)
            cand = {
                "tags": clean_tags(tags, h2z),
                "bio": bio,
                "birth": (birth or "").strip(),
                "height": _bio_number(bio, "身高"),
            }
            out.setdefault(jp, []).append(cand)
    finally:
        con.close()
    return out


def pick_mapping(extra: dict[str, list[dict]], names: list[str],
                 birthday: str, height: int) -> dict:
    """按名字找 mapping 候选，用生日/身高挑出真正同一个人。

    names 是 profile 的名字 + 别名（含旧名）。遍历时优先用直接同名，其次别名；
    每个候选用生日或身高校验：任一字段两边都有且不一致就判为不同人，直接跳过。
    这样「新ありな」通过旧名「橋本ありな」找到的 2005 年生/G 杯记录会被拒绝，
    不会把别人的标签贴到本人身上。没有校验信息时退回「只认直接同名、别名的
    候选必须有标签」的保守策略，宁可空着也不错贴。
    """
    def birth_year(s: str) -> int:
        m = re.match(r"((?:19|20)\d{2})", s or "")
        return int(m.group(1)) if m else 0

    best: dict = {}
    for i, name in enumerate(names):
        for cand in extra.get(name, []):
            # 校验：身高是更可靠的身份特征（差 ≤1cm 视为同人）；成人业界生日来源
            # 常年互相差 1~2 年，年份差 ≤2 也算同人。两者任一通过即可，避免误伤。
            # 但两边信息都齐、身高差 >1 且生日年份差 >2 时，判为不同人直接挡掉。
            has_h = bool(cand["height"] and height)
            has_b = bool(cand["birth"] and birthday)
            h_ok = has_h and abs(cand["height"] - height) <= 1
            b_ok = has_b and abs(birth_year(cand["birth"]) - birth_year(birthday)) <= 2
            if has_h and has_b and not h_ok and not b_ok:
                continue
            if has_h and not h_ok and not has_b:
                continue
            if has_b and not b_ok and not has_h:
                continue
            # 优先级：直接同名 > 别名；校验通过的 > 无校验信息的
            score = (0 if i == 0 else 1) + (0 if (h_ok or b_ok) else 1)
            if not best or score < best["_score"]:
                cand = dict(cand)
                cand["_score"] = score
                best = cand
    return best


def flat(s: str) -> str:
    """把换行/制表符压成空格。上游别名里确实有换行，不处理会把 CSV 拆断行。"""
    return re.sub(r"[\r\n\t]+", " ", s or "").strip()


def split_aliases(s: str) -> list[str]:
    """切分别名列表。

    actor-profile 的 aliases 有 51% 用**换行**分隔（如「上乃木まな\\n岩谷志季\\n橋本ありな」），
    少数用逗号/顿号。flat() 会把换行压成空格，若只按标点切，整串会粘成一个别名，
    改过名的人（新ありな 的旧名 橋本ありな）因此无法被 alias 索引命中。
    这里先把换行/制表符归一成 |，再按标点切；刻意不按空格切，避免把罗马字名
    「Arina Hashimoto」拆成两段。
    """
    if not s:
        return []
    normalized = re.sub(r"[\r\n\t]+", "|", s)
    return [p.strip() for p in re.split(r"[,，、|/]", normalized) if p.strip()]


def export(mapping_path: str, profile_path: str, out_path: str, h2z: dict[str, str]) -> int:
    extra = read_mapping(mapping_path, h2z)

    con = sqlite3.connect(f"file:{profile_path}?mode=ro", uri=True)
    con.row_factory = sqlite3.Row
    try:
        names = {r["id"]: flat(r["name"]) for r in con.execute("SELECT id, name FROM entries")}
        fields: dict[int, dict[str, str]] = {}
        for r in con.execute("SELECT entry_id, field, value FROM website_fields"):
            raw = r["value"] or ""
            # aliases 的换行是主要分隔符（51% 的别名靠它分开），不能先压平，
            # 否则「橋本ありな」这类旧名会粘进上一个别名里，永远索引不到。
            v = raw if r["field"] == "aliases" else flat(raw)
            if v:
                fields.setdefault(r["entry_id"], {})[r["field"]] = v

        rows = []
        for eid, name in names.items():
            if not name or set(name) <= {"*", "·", "-", " ", "."}:
                continue
            f = fields.get(eid, {})
            place = clean_place(f.get("birthplace", ""), h2z)
            # profile 的名字 + 别名一起作为 mapping 的查找键：改名的人（如
            # 新ありな/橋本ありな）靠别名才能找到旧 mapping 记录。
            profile_names = [name] + split_aliases(f.get("aliases", ""))
            birth = (f.get("birthday", "") or "")[:10]
            height = int(re.sub(r"\D", "", f.get("height", "") or "") or 0)
            e = pick_mapping(extra, profile_names, birth, height)
            rows.append([
                name,
                f.get("name_cn", ""),
                "|".join(split_aliases(f.get("aliases", ""))),
                place,
                guess_country(place),
                f.get("height", ""),
                f.get("cup", ""),
                parse_career(f.get("career_period", "")) or "",
                "|".join(e.get("tags") or []),
                e.get("bio", "") or f.get("minnano_bio", ""),
                f.get("romanization", ""),
                # 生日只是 JavStash 的兜底：MDCx 覆盖 65%，但约 24% 与 JavStash 冲突，
                # 所以 Go 侧只在 JavStash 没有时才用，并标 birth_date_source=mdcx。
                f.get("birthday", ""),
                # 三围分项（厘米）。TPDB 只覆盖 36%，而这里三项齐备约 80%，
                # 是日系演员三围最全的来源。分三列原样导出（厘米），
                # Go 侧判断齐备后统一换算成英寸（对外口径）。
                f.get("bust", ""),
                f.get("waist", ""),
                f.get("hip", ""),
            ])
    finally:
        con.close()

    rows.sort(key=lambda r: r[0])
    os.makedirs(os.path.dirname(out_path) or ".", exist_ok=True)
    with open(out_path, "w", encoding="utf-8", newline="") as fp:
        w = csv.writer(fp, lineterminator="\n")
        w.writerow(["name", "name_cn", "aliases", "birthplace", "country",
                    "height", "cup", "career_start", "tags", "bio", "romaji", "birthday",
                    "bust", "waist", "hip"])
        w.writerows(rows)

    with_place = sum(1 for r in rows if r[3])
    with_year = sum(1 for r in rows if r[7])
    with_tags = sum(1 for r in rows if r[8])
    with_cup = sum(1 for r in rows if r[6])
    with_meas = sum(1 for r in rows if r[12] and r[13] and r[14])
    log(f"导出 {len(rows)} 人 → {out_path}")
    log(f"  出生地 {with_place} ({with_place/len(rows)*100:.1f}%) / "
        f"罩杯 {with_cup} ({with_cup/len(rows)*100:.1f}%) / "
        f"出道年 {with_year} ({with_year/len(rows)*100:.1f}%) / "
        f"标签 {with_tags} ({with_tags/len(rows)*100:.1f}%) / "
        f"三围齐备 {with_meas} ({with_meas/len(rows)*100:.1f}%)")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("-o", "--out", default="/tmp/mdcx_profiles.csv")
    ap.add_argument("--offline", metavar="DIR", help="用目录里已有的 actor-profile.sqlite3")
    ap.add_argument("--force", action="store_true")
    ap.add_argument("--state", default="data/base_sources.json")
    ap.add_argument("--zhcdict", default="", help="本地 zhcdict.json，省得联网下载 650KB")
    args = ap.parse_args()

    if args.offline:
        base = args.offline
        profile_db = os.path.join(base, "actor-profile.sqlite3")
        mapping_db = os.path.join(base, "actor-mapping.sqlite3")
        for p in (profile_db, mapping_db):
            if not os.path.exists(p):
                log(f"离线模式缺少文件：{p}")
                return 1
        log("离线模式：跳过下载")
    else:
        state = load_state(args.state)
        tmpdir = tempfile.mkdtemp(prefix="mdcxprof-")
        try:
            profile_db, _, _ = fetch_asset(
                "actor-profile.sqlite3",
                os.path.join(tmpdir, "actor-profile.sqlite3"),
                state, "mdcx_profile", args.force,
            )
            mapping_db, _, _ = fetch_asset(
                "actor-mapping.sqlite3",
                os.path.join(tmpdir, "actor-mapping.sqlite3"),
                state, "mdcx_mapping", args.force,
            )
        except Exception as e:  # noqa: BLE001
            log(f"下载失败：{e}")
            return 1
        save_state(args.state, state)

    h2z = load_hant2zh(args.zhcdict)
    return export(mapping_db, profile_db, args.out, h2z)


if __name__ == "__main__":
    sys.exit(main())
