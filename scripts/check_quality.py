#!/usr/bin/env python3
"""演员数据质量体检：把 actors.jsonl 里「不合法、互相矛盾、明显错配」的行挑出来。

用法：
    python3 scripts/check_quality.py                 # 读仓库根目录的 actors.jsonl
    python3 scripts/check_quality.py path/to/actors.jsonl
    python3 scripts/check_quality.py --json out.json  # 顺带落一份机器可读结果
    python3 scripts/check_quality.py --baseline last.json  # 与上次结果比对覆盖率

设计原则：
  1. 只读，不改任何数据；
  2. 每条检查都给「命中数 + 总分母 + 前几条样本」，样本用 key 而不是索引，方便直接去查；
  3. 有问题时退出码 1，方便挂在 CI 上当闸门（默认只在超过阈值时才报错）。
"""

import json
import re
import sys
import argparse
import collections
from datetime import date

# ---------- 认为合理的区间，越界只提示不阻断 ----------
MIN_BIRTH_YEAR = 1940
MIN_DEBUT_AGE = 14          # 出道时未成年 = 生日或出道年至少有一个错
HEIGHT_RANGE = (130, 200)
IN_RANGE = {"bust": (25, 55), "waist": (18, 45), "hip": (28, 55)}
CM_RANGE = {"bust": (60, 130), "waist": (45, 110), "hip": (65, 135)}
CUP_LETTERS = set("ABCDEFGHIJKLMNOP")
TAG_BLACKLIST = ("未成年", "幼児体型", "幼儿体型", "儿童")
BIO_MIN, BIO_MAX = 20, 3000


def load(path):
    rows, bad = [], []
    with open(path, encoding="utf-8") as f:
        for i, line in enumerate(f, 1):
            line = line.strip()
            if not line:
                continue
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError as e:
                bad.append((i, str(e)))
    return rows, bad


def exportable(a):
    """与 Go 侧 Actor.Exportable() 保持同一口径。"""
    if a.get("status") not in ("ok", "partial"):
        return False
    name = a.get("name") or ""
    return "_" not in name


def pct(n, d):
    return f"{n / d:.1%}" if d else "-"


class Report:
    def __init__(self, total, exp):
        self.total = total
        self.exp = exp
        self.checks = []   # (标题, 命中, 分母, 说明, 样本)
        self.cover = []    # (字段, 命中, 说明)

    def check(self, title, hits, denom, note="", samples=None):
        self.checks.append((title, len(hits), denom, note, hits[:5]))

    def coverage(self, field, hits, note=""):
        self.cover.append((field, hits, note))

    def render(self):
        out = []
        out.append("## 1. 字段覆盖率（分母 = 可导出记录）\n")
        out.append("| 字段 | 有值 | 占比 | 备注 |")
        out.append("|---|---:|---:|---|")
        for field, hits, note in self.cover:
            out.append(f"| `{field}` | {hits} | {pct(hits, self.exp)} | {note} |")

        out.append("\n## 2. 异常与矛盾\n")
        out.append("| 检查项 | 命中 | 分母 | 占比 | 说明 |")
        out.append("|---|---:|---:|---:|---|")
        for title, n, d, note, _ in self.checks:
            out.append(f"| {title} | {n} | {d} | {pct(n, d)} | {note} |")

        bad = [(t, n, s) for t, n, d, note, s in self.checks if n and s]
        if bad:
            out.append("\n## 3. 样例\n")
            for title, n, samples in bad:
                out.append(f"**{title}**（{n} 条，前 {len(samples)} 条）\n")
                for s in samples:
                    out.append(f"- `{s}`")
                out.append("")
        return "\n".join(out)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("path", nargs="?", default="actors.jsonl")
    ap.add_argument("--json", dest="json_out")
    ap.add_argument("--baseline")
    ap.add_argument("--today", default=date.today().isoformat())
    args = ap.parse_args()

    rows, bad_json = load(args.path)
    if not rows:
        print(f"读不到任何记录：{args.path}", file=sys.stderr)
        return 2

    exp = [a for a in rows if exportable(a)]
    rep = Report(len(rows), len(exp))

    # ---------- 结构 ----------
    status = collections.Counter(a.get("status") or "(空)" for a in rows)
    keys = [a.get("key") for a in rows if a.get("key")]
    dup_keys = [k for k, c in collections.Counter(keys).items() if c > 1]
    name_counts = collections.Counter((a.get("name") or "").strip() for a in exp)
    dup_names = [f"{n} ×{c}" for n, c in name_counts.items() if c > 1]

    rep.check("JSON 解析失败", bad_json, len(rows), "行级损坏，整行丢弃",
              [f"第 {i} 行：{e}" for i, e in bad_json])
    rep.check("key 重复", dup_keys, len(rows), "同一 key 出现多行")
    rep.check("导出记录重名", dup_names, len(exp), "同名多条 = Emby 里会重复")
    nameless = [a.get("key", "?") for a in exp if not (a.get("name") or "").strip()]
    rep.check("导出记录无名字", nameless, len(exp), "Emby 无法匹配")

    # ---------- 覆盖率 ----------
    n = len(exp)
    def cov(field):
        return sum(1 for a in exp if a.get(field) not in (None, "", [], {}))

    now_year = date.today().year
    rep.coverage("name_original", cov("name_original"), "日文原名")
    rep.coverage("birth_date", cov("birth_date"), "Emby BirthDate")
    rep.coverage("birth_place", cov("birth_place"), "Emby ProductionLocations")
    rep.coverage("country", cov("country"))
    rep.coverage("height_cm", cov("height_cm"))
    rep.coverage("measurements", cov("measurements"))
    rep.coverage("cup_size", cov("cup_size"))
    rep.coverage("career_start", cov("career_start"), "Emby ProductionYear")
    rep.coverage("bio", cov("bio"), "Emby Overview")
    rep.coverage("avatar", cov("avatar"))
    rep.coverage("aliases", cov("aliases"))
    rep.coverage("tags", cov("tags"))
    rep.coverage("homepage_url", cov("homepage_url"))
    rep.coverage("provider_ids", cov("provider_ids"))
    rep.coverage("birth_date_source", cov("birth_date_source"), "谁给的生日")

    # ---------- 生日 ----------
    bad_birth, weird_birth, future_birth = [], [], []
    iso = re.compile(r"^(\d{4})-(\d{2})-(\d{2})$")
    for a in exp:
        b = a.get("birth_date")
        if not b:
            continue
        m = iso.match(b)
        key = a.get("key", a.get("name"))
        if not m:
            bad_birth.append(f"{key} = {b}")
            continue
        y, mo, d = int(m.group(1)), int(m.group(2)), int(m.group(3))
        if not (1 <= mo <= 12 and 1 <= d <= 31):
            bad_birth.append(f"{key} = {b}")
        elif not (MIN_BIRTH_YEAR <= y <= now_year):
            weird_birth.append(f"{key} = {b}")
        elif date(y, mo, d) > date.today():
            future_birth.append(f"{key} = {b}")
    rep.check("生日格式非法", bad_birth, n, "非 YYYY-MM-DD 或月日越界")
    rep.check("生日年份离谱", weird_birth, n, f"不在 {MIN_BIRTH_YEAR}–{now_year}")
    rep.check("生日在未来", future_birth, n, "过生日了还没出生")

    # ---------- 出道年 ----------
    bad_debut, future_debut = [], []
    for a in exp:
        c = a.get("career_start")
        if not isinstance(c, int):
            continue
        key = a.get("key", a.get("name"))
        if c < 1950 or c > now_year:
            bad_debut.append(f"{key} = {c}")
    rep.check("出道年越界", bad_debut, n, f"不在 1950–{now_year}")

    young = []
    for a in exp:
        b, c = a.get("birth_date"), a.get("career_start")
        if not b or not isinstance(c, int):
            continue
        m = iso.match(b)
        if not m:
            continue
        if c - int(m.group(1)) < MIN_DEBUT_AGE:
            young.append(f"{a.get('key', a.get('name'))} 生于 {b}、出道 {c}（{c - int(m.group(1))} 岁）")
    rep.check("出道年龄不足 14 岁", young, n, "生日或出道年至少一个错")

    # ---------- 身高 ----------
    bad_h = [f"{a.get('key')} = {a.get('height_cm')}" for a in exp
             if isinstance(a.get("height_cm"), int)
             and not (HEIGHT_RANGE[0] <= a["height_cm"] <= HEIGHT_RANGE[1])]
    rep.check("身高越界", bad_h, n, f"不在 {HEIGHT_RANGE[0]}–{HEIGHT_RANGE[1]} cm")

    # ---------- 三围 ----------
    meas_re = re.compile(r"^(\d{2,3})([A-Z]{1,3})?-(\d{2,3})-(\d{2,3})$")
    bad_meas, odd_meas, rel_meas = [], [], []
    for a in exp:
        m = a.get("measurements")
        if not m:
            continue
        key = a.get("key", a.get("name"))
        mm = meas_re.match(m.strip())
        if not mm:
            bad_meas.append(f"{key} = {m}")
            continue
        unit = (a.get("measurements_unit") or "in").lower()
        rng = CM_RANGE if unit == "cm" else IN_RANGE
        bust, waist, hip = int(mm.group(1)), int(mm.group(3)), int(mm.group(4))
        for label, v in (("bust", bust), ("waist", waist), ("hip", hip)):
            lo, hi = rng[label]
            if not (lo <= v <= hi):
                odd_meas.append(f"{key} = {m}（{label} {v} 超出 {lo}–{hi}{unit}）")
                break
        else:
            if waist >= bust or hip < waist:
                rel_meas.append(f"{key} = {m}")
    rep.check("三围格式非法", bad_meas, n, "应形如 34E-24-35 / 44-25-39")
    rep.check("三围数值越界", odd_meas, n, "按 measurements_unit 判区间")
    rep.check("三围关系矛盾", rel_meas, n, "腰≥胸 或 臀<腰")

    # ---------- 罩杯 ----------
    bad_cup = []
    for a in exp:
        c = a.get("cup_size")
        if not c:
            continue
        c = c.strip().upper()
        # 单字母 A-O（日标）或多字母重复（AA/DD/JJ/DDD，欧美杯型）
        valid = (1 <= len(c) <= 3 and c in CUP_LETTERS) or (len(c) in (2, 3) and len(set(c)) == 1 and c[0] in CUP_LETTERS)
        if not valid:
            bad_cup.append(f"{a.get('key')} = {a.get('cup_size')}")
    rep.check("罩杯非法", bad_cup, n, "接受 A–O 单字母或 AA/DD/JJ/DDD 重复字母")

    # 罩杯标签一致性
    no_tag, wrong_tag = [], []
    for a in exp:
        cup = (a.get("cup_size") or "").strip().upper()
        tags = a.get("tags") or []
        cups_in_tags = [t for t in tags if t.endswith("罩杯")]
        if cup and not cups_in_tags:
            no_tag.append(f"{a.get('key')} cup={cup} tags={tags[:4]}")
        elif cup and cups_in_tags and f"{cup}罩杯" not in cups_in_tags:
            wrong_tag.append(f"{a.get('key')} cup={cup} tags={cups_in_tags}")
    rep.check("有罩杯但标签缺罩杯", no_tag, n, "罩杯标签没补上")
    rep.check("标签罩杯与字段不符", wrong_tag, n, "两处口径不一致")

    # ---------- 出生地 ----------
    unqualified, unknown_place = [], []
    pref = re.compile(r"(都|府|县|県)$")
    for a in exp:
        p = a.get("birth_place")
        if not p:
            continue
        key = a.get("key", a.get("name"))
        if pref.search(p) and "·" not in p:
            unqualified.append(f"{key} = {p}")
        elif "·" in p:
            country, _, region = p.partition("·")
            if not country.strip() or not region.strip():
                unknown_place.append(f"{key} = {p}")
    rep.check("出生地未带国家", unqualified, n, "都道府县必须写成「日本·东京都」")
    rep.check("出生地格式残缺", unknown_place, n, "「·」前或后为空")

    # ---------- 简介 ----------
    bad_bio, dup_bio, jap_bio = [], [], []
    seen_bio = {}
    for a in exp:
        b = a.get("bio")
        if not b:
            continue
        key = a.get("key", a.get("name"))
        L = len(b)
        if L < BIO_MIN or L > BIO_MAX:
            bad_bio.append(f"{key} 长度 {L}：{b[:40]}")
        kana = len(re.findall(r"[\u3040-\u30ff]", b))
        if L and kana / L > 0.2:
            jap_bio.append(f"{key} 假名占 {kana / L:.0%}：{b[:40]}")
        norm = re.sub(r"\s+", "", b)[:80]
        if norm in seen_bio and seen_bio[norm] != key:
            dup_bio.append(f"{key} 与 {seen_bio[norm]} 简介前 80 字相同")
        else:
            seen_bio[norm] = key
    rep.check("简介长度异常", bad_bio, n, f"不在 {BIO_MIN}–{BIO_MAX} 字")
    rep.check("简介疑似日文", jap_bio, n, "假名占比 > 20%，应当只要中文")
    rep.check("简介重复", dup_bio, n, "同一段简介挂在多人身上")

    # ---------- 标签 ----------
    black, empty_tags = [], []
    for a in exp:
        tags = a.get("tags") or []
        for t in tags:
            if any(w in t for w in TAG_BLACKLIST):
                black.append(f"{a.get('key')} tag={t}")
        if not tags:
            empty_tags.append(a.get("key"))
    rep.check("标签含黑名单词", black, n, "未成年相关词不得进元数据")
    rep.check("无标签", empty_tags, n, "MDCx 没有标签的人")

    # ---------- 别名 ----------
    self_alias, ja_alias, too_many = [], [], []
    for a in exp:
        al = [(t or "").strip() for t in (a.get("aliases") or [])]
        name = (a.get("name") or "").strip()
        orig = (a.get("name_original") or "").strip()
        low = [t.lower() for t in al]
        if name and name.lower() in low:
            self_alias.append(f"{a.get('key')} 主名 {name}")
        if orig and orig.lower() in low:
            ja_alias.append(f"{a.get('key')} {orig}")
        if len(al) > 12:
            too_many.append(f"{a.get('key')} 别名 {len(al)} 个")
    # 主名重复是真冗余（下游按名字匹配会绕圈）；日文原名重复只是啰嗦，无害
    rep.check("别名含本人主名", self_alias, n, "中文主名又出现在别名里")
    rep.check("别名含日文原名", ja_alias, n, "冗余但无害，可忽略")
    rep.check("别名过多", too_many, n, "> 12 个别名")

    # ---------- 跨字段一致性 ----------
    cup_conflict = []
    for a in exp:
        m = a.get("measurements")
        cup = (a.get("cup_size") or "").strip().upper()
        if not m or not cup:
            continue
        mm = meas_re.match(m.strip())
        if mm and mm.group(2) and mm.group(2) != cup:
            cup_conflict.append(f"{a.get('key')} 三围 {m} 但 cup_size={cup}")
    rep.check("三围首段罩杯与字段不符", cup_conflict, n, "两个源各写各的，下游不知信谁")

    country_mismatch = []
    country_cn = {"JP": "日本", "CN": "中国", "KR": "韩国", "US": "美国",
                  "TW": "中国台湾", "HK": "中国香港", "MO": "中国澳门"}
    for a in exp:
        p, c = a.get("birth_place"), a.get("country")
        if not p or not c or "·" not in p:
            continue  # 没带国家的由上面那条检查负责
        head = p.split("·")[0].strip()
        want = country_cn.get(c.upper())
        if want and head and head != want:
            country_mismatch.append(f"{a.get('key')} birth_place={p} country={c}")
    rep.check("出生地国家与 country 码不符", country_mismatch, n, "同一个事实两处矛盾")

    # ---------- 头像 ----------
    dup_avatar, http_avatar = [], []
    by_avatar = {}
    for a in exp:
        u = a.get("avatar")
        if not u:
            continue
        key = a.get("key", a.get("name"))
        if u.startswith("http://"):
            http_avatar.append(f"{key} {u[:60]}")
        if u in by_avatar:
            dup_avatar.append(f"{key} 与 {by_avatar[u]} 同图")
        else:
            by_avatar[u] = key
    rep.check("头像重复", dup_avatar, cov("avatar"), "多人同一张图 = 错配")
    rep.check("头像非 https", http_avatar, n, "Emby 拉图可能失败")

    # ---------- provider_ids ----------
    pid_conflict = []
    owner = {}
    for a in exp:
        for site, pid in (a.get("provider_ids") or {}).items():
            if not pid:
                continue
            k = (site, pid)
            if k in owner and owner[k] != a.get("key"):
                pid_conflict.append(f"{site}:{pid} 同时属于 {owner[k]} 和 {a.get('key')}")
            else:
                owner[k] = a.get("key")
    rep.check("同一站点 ID 指向多人", pid_conflict, n, "匹配到了错误的人")

    # ---------- 来源分布 ----------
    src = collections.Counter()
    for a in exp:
        for s, ok in (a.get("sources") or {}).items():
            if ok:
                src[s] += 1

    # ---------- 输出 ----------
    print(f"# 演员数据质量体检\n")
    print(f"- 数据文件：`{args.path}`")
    print(f"- 体检日期：{args.today}")
    print(f"- 总记录：{len(rows)}，可导出：{len(exp)}")
    print(f"- 状态分布：" + "、".join(f"{k} {v}" for k, v in status.most_common()))
    print(f"- 来源命中（可导出内）：" + "、".join(f"{k} {v}" for k, v in src.most_common()))
    print()
    print(rep.render())

    result = {
        "date": args.today,
        "path": args.path,
        "total": len(rows),
        "exportable": len(exp),
        "status": dict(status),
        "sources": dict(src),
        "coverage": {f: c for f, c, _ in rep.cover},
        "checks": {t: n for t, n, d, note, s in rep.checks},
    }

    if args.baseline:
        try:
            base = json.load(open(args.baseline, encoding="utf-8"))
            print("\n## 4. 与上次对比\n")
            print("| 字段 | 上次 | 本次 | 变化 |")
            print("|---|---:|---:|---:|")
            for f, c in result["coverage"].items():
                old = base.get("coverage", {}).get(f)
                if old is None:
                    continue
                d = c - old
                print(f"| `{f}` | {old} | {c} | {d:+d} |")
            print("\n| 检查项 | 上次 | 本次 | 变化 |")
            print("|---|---:|---:|---:|")
            for f, c in result["checks"].items():
                old = base.get("checks", {}).get(f)
                if old is None:
                    continue
                print(f"| {f} | {old} | {c} | {c - old:+d} |")
        except (OSError, json.JSONDecodeError) as e:
            print(f"\n（基线读取失败，跳过对比：{e}）")

    if args.json_out:
        with open(args.json_out, "w", encoding="utf-8") as f:
            json.dump(result, f, ensure_ascii=False, indent=2)
            f.write("\n")
        print(f"\n结果已写入 {args.json_out}")

    hard = ("生日格式非法", "三围格式非法", "罩杯非法", "出生地未带国家", "同一站点 ID 指向多人")
    fails = [t for t, n, d, note, s in rep.checks if t in hard and n]
    if fails:
        print(f"\n❌ 需要处理：{'、'.join(fails)}", file=sys.stderr)
        return 1
    print("\n✅ 硬性问题：0")
    return 0


if __name__ == "__main__":
    sys.exit(main())
