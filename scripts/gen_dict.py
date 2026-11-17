#!/usr/bin/env python3
"""从 amane 的 mapping_actor.xml 生成中文名 → 日文名字典。

用法：
    python3 scripts/gen_dict.py                  # 写入 data/actor_dict.csv
    python3 scripts/gen_dict.py -o /tmp/x.csv    # 自定义输出

生成结果是主字典，随时可重新生成。不要手改它——
手工补充请写到 data/actor_dict.local.csv，加载时会覆盖主字典。
"""
import argparse
import csv
import io
import sys
import urllib.request
import xml.etree.ElementTree as ET

SRC = "https://raw.githubusercontent.com/sqzw-x/amane/main/resources/mapping_table/mapping_actor.xml"


def fetch(url: str) -> bytes:
    req = urllib.request.Request(url, headers={"User-Agent": "actor-em/1.0"})
    with urllib.request.urlopen(req, timeout=120) as r:
        return r.read()


def build(xml_bytes: bytes) -> dict:
    root = ET.fromstring(xml_bytes)
    rows: dict[str, str] = {}
    for a in root.iter("a"):
        jp = (a.get("jp") or "").strip()
        if not jp:
            continue
        jp_first = jp.split(",")[0].strip()
        if not jp_first:
            continue

        keys = [(a.get("zh_cn") or "").strip(), (a.get("zh_tw") or "").strip()]
        # keyword 里放的是用于匹配的别名，同样是有效的查询键
        keys += [k.strip() for k in (a.get("keyword") or "").split(",")]

        for k in keys:
            if k and k != jp_first:
                rows.setdefault(k, jp_first)
    return rows


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("-o", "--output", default="data/actor_dict.csv")
    ap.add_argument("-i", "--input", help="本地 XML 文件（默认从网络下载）")
    args = ap.parse_args()

    if args.input:
        with open(args.input, "rb") as f:
            xml_bytes = f.read()
    else:
        try:
            xml_bytes = fetch(SRC)
        except Exception as e:
            print(f"下载失败: {e}", file=sys.stderr)
            return 1

    rows = build(xml_bytes)
    if not rows:
        print("解析结果为空，放弃写入", file=sys.stderr)
        return 1

    buf = io.StringIO()
    w = csv.writer(buf, lineterminator="\n")
    w.writerow(["中文名", "日文名"])
    for k in sorted(rows):
        w.writerow([k, rows[k]])

    with open(args.output, "w", encoding="utf-8", newline="") as f:
        f.write(buf.getvalue())

    print(f"写入 {args.output}：{len(rows)} 条")
    return 0


if __name__ == "__main__":
    sys.exit(main())
