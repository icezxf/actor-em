#!/usr/bin/env python3
"""工作流 1：基础数据库更新。

合并两个上游，产出仓库根目录的 actor_dict.csv（四列：别名, 日文名, 中文名, 来源）。

数据源：
  - amane   mapping_actor.xml   中日名映射，别名最全，但更新很慢（2026-08 后无提交）
  - MDCx    actor-mapping.sqlite3  中文名覆盖率 99.9%，补 amane 的缺

增量策略：
  - MDCx 走 GitHub Release API，比对 asset 的 sha256 digest 与本地记录，相同就不下载（省 6MB）
  - amane 走 HTTP ETag，304 就直接跳过
  - 两源都没变，脚本 exit 0 且不写文件，工作流随之跳过提交

用法：
    python3 scripts/build_base_db.py                    # 正常跑
    python3 scripts/build_base_db.py --force            # 忽略缓存，强制重下
    python3 scripts/build_base_db.py --offline DIR      # 用本地已有的 XML/sqlite，不联网
    python3 scripts/build_base_db.py --dry-run          # 只统计不落盘
"""
from __future__ import annotations

import argparse
import csv
import hashlib
import io
import json
import os
import re
import sqlite3
import sys
import tempfile
import time
import urllib.error
import urllib.request
import xml.etree.ElementTree as ET

AMANE_XML = "https://raw.githubusercontent.com/sqzw-x/amane/main/resources/mapping_table/mapping_actor.xml"
MDCX_API = "https://api.github.com/repos/xiarenyyds/databases/releases/latest"

OUT_CSV = "actor_dict.csv"
STATE_JSON = "data/base_sources.json"

UA = "actor-em/1.0 (+https://github.com/icezxf/actor-em)"

# 清洗规则：这些键是上游用来标注「易混淆」「同名异人」的标记，不是可查询的名字
MARK_RE = re.compile(r"[【】\[\]]")
BAD_KEY_RE = re.compile(r"^[\s\W_]+$", re.UNICODE)


def log(msg: str) -> None:
    print(msg, file=sys.stderr, flush=True)


def sha256_file(path: str) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def load_state(path: str) -> dict:
    try:
        with open(path, encoding="utf-8") as f:
            return json.load(f)
    except (OSError, ValueError):
        return {}


def save_state(path: str, state: dict) -> None:
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        json.dump(state, f, ensure_ascii=False, indent=2, sort_keys=True)
        f.write("\n")


def http_get(url: str, headers: dict | None = None, timeout: int = 180) -> tuple[bytes, dict]:
    req = urllib.request.Request(url, headers={"User-Agent": UA, **(headers or {})})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        return r.read(), dict(r.headers)


def fetch_amane(dest: str, state: dict, force: bool) -> tuple[str, bool]:
    """返回 (本地路径, 是否更新过)。ETag 命中就复用本地缓存。"""
    etag = state.get("amane", {}).get("etag")
    if not force and etag and os.path.exists(dest):
        try:
            req = urllib.request.Request(
                AMANE_XML, headers={"User-Agent": UA, "If-None-Match": etag}, method="GET"
            )
            with urllib.request.urlopen(req, timeout=120) as r:
                body = r.read()
                new_etag = r.headers.get("ETag", "")
        except urllib.error.HTTPError as e:
            if e.code == 304:
                log("amane: 304 未变更，复用缓存")
                return dest, False
            raise
        with open(dest, "wb") as f:
            f.write(body)
        state["amane"] = {"etag": new_etag, "sha256": hashlib.sha256(body).hexdigest()}
        log(f"amane: 已更新（{len(body)/1024:.0f} KB）")
        return dest, True

    body, headers = http_get(AMANE_XML)
    with open(dest, "wb") as f:
        f.write(body)
    state["amane"] = {"etag": headers.get("ETag", ""), "sha256": hashlib.sha256(body).hexdigest()}
    log(f"amane: 已下载（{len(body)/1024:.0f} KB）")
    return dest, True


def download(url: str, timeout: int = 600) -> bytes:
    """下载大文件：直连 → 重试 → ghproxy 镜像。

    GitHub 的 release 文件落在 objects.githubusercontent.com，国内经常被掐断
    （Remote end closed connection without response），所以留一条镜像退路。
    """
    last_exc: Exception | None = None
    for prefix in ("", "https://ghproxy.net/"):
        for attempt in range(1, 3):
            try:
                return http_get(prefix + url, timeout=timeout)[0]
            except Exception as e:  # noqa: BLE001
                last_exc = e
                log(f"  下载失败（{prefix or '直连'} 第 {attempt} 次）：{type(e).__name__}: {e}")
                time.sleep(2 * attempt)
    raise last_exc  # type: ignore[misc]


def fetch_asset(asset_name: str, dest: str, state: dict, state_key: str, force: bool) -> tuple[str, bool, str]:
    """从 MDCx 的 release 里挑一个 asset 下下来。

    返回 (本地路径, 是否有更新, release tag)。按 asset 的 sha256 digest 做增量判断。
    """
    meta, _ = http_get(MDCX_API, headers={"Accept": "application/vnd.github+json"})
    release = json.loads(meta)
    asset = next((a for a in release.get("assets", []) if a["name"] == asset_name), None)
    if asset is None:
        raise RuntimeError(f"release 里找不到 {asset_name}")

    digest = (asset.get("digest") or "").removeprefix("sha256:")
    tag = release.get("tag_name", "")
    prev = state.get(state_key, {})
    if not force and digest and prev.get("sha256") == digest and os.path.exists(dest):
        log(f"{asset_name}: digest 未变（{tag}），复用缓存")
        return dest, False, tag

    log(f"{asset_name}: 下载 {tag} / {asset['size']/1024/1024:.1f} MB …")
    body = download(asset["browser_download_url"])
    got = hashlib.sha256(body).hexdigest()
    if digest and got != digest:
        raise RuntimeError(f"{asset_name} sha256 校验失败：期望 {digest}，实际 {got}")
    with open(dest, "wb") as f:
        f.write(body)
    state[state_key] = {"tag": tag, "sha256": got, "updated_at": asset.get("updated_at", "")}
    log(f"{asset_name}: 完成（{len(body)/1024/1024:.1f} MB，sha256 校验通过）")
    return dest, True, tag


def fetch_mdcx(dest: str, state: dict, force: bool) -> tuple[str, bool]:
    """只下 actor-mapping（词典用）。profile 是 33MB，留给工作流 2。"""
    path, changed, _ = fetch_asset("actor-mapping.sqlite3", dest, state, "mdcx_mapping", force)
    return path, changed


def clean_key(k: str) -> str:
    k = (k or "").replace("\u3000", " ").strip()
    if not k or len(k) < 2 or MARK_RE.search(k) or k.startswith("@") or BAD_KEY_RE.match(k):
        return ""
    return k


def split_multi(v: str) -> list[str]:
    return [s.strip() for s in re.split(r"[,，、/|]", v or "") if s.strip()]


def parse_amane(path: str) -> list[tuple[str, str, str, str]]:
    """返回 [(别名, 日文名, 中文名, 来源)]"""
    root = ET.parse(path).getroot()
    rows = []
    for a in root.iter("a"):
        jp = (a.get("jp") or "").split(",")[0].strip()
        if not jp:
            continue
        zh = (a.get("zh_cn") or "").strip()
        keys = [zh, (a.get("zh_tw") or "").strip()] + split_multi(a.get("keyword"))
        for k in keys:
            k = clean_key(k)
            if k:
                rows.append((k, jp, zh, "amane"))
    return rows


def parse_mdcx(path: str) -> list[tuple[str, str, str, str]]:
    con = sqlite3.connect(f"file:{path}?mode=ro", uri=True)
    try:
        cur = con.execute("SELECT jp, zh_cn, zh_tw, keyword FROM entries")
        rows = []
        for jp, zh_cn, zh_tw, keyword in cur:
            jp = (jp or "").split(",")[0].strip()
            if not jp:
                continue
            zh = (zh_cn or "").strip()
            keys = [zh, (zh_tw or "").strip()] + split_multi(keyword)
            for k in keys:
                k = clean_key(k)
                if k:
                    rows.append((k, jp, zh, "mdcx"))
        return rows
    finally:
        con.close()


def build(amane_rows, mdcx_rows) -> list[tuple[str, str, str, str]]:
    """amane 优先，MDCx 填空。同一个别名两家冲突时保留 amane。"""
    merged: dict[str, tuple[str, str, str]] = {}
    for k, jp, zh, src in mdcx_rows:
        merged[k] = (jp, zh, src)
    for k, jp, zh, src in amane_rows:
        prev = merged.get(k)
        # amane 权威，但中文名优先用它自己的，没有就继承 MDCx 的
        merged[k] = (jp, zh or (prev[1] if prev and prev[0] == jp else ""), src)
    out = [(k, v[0], v[1], v[2]) for k, v in merged.items()]
    out.sort(key=lambda r: (r[1], r[0]))
    return out


def write_csv(path: str, rows) -> bool:
    buf = io.StringIO()
    w = csv.writer(buf, lineterminator="\n")
    w.writerow(["别名", "日文名", "中文名", "来源"])
    w.writerows(rows)
    text = buf.getvalue()

    old = ""
    if os.path.exists(path):
        with open(path, encoding="utf-8") as f:
            old = f.read()
    if old == text:
        return False
    with open(path, "w", encoding="utf-8", newline="") as f:
        f.write(text)
    return True


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--force", action="store_true", help="忽略缓存强制下载")
    ap.add_argument("--offline", metavar="DIR", help="用目录里已有的 mapping_actor.xml / actor-mapping.sqlite3")
    ap.add_argument("--dry-run", action="store_true", help="只统计，不写文件")
    ap.add_argument("--out", default=OUT_CSV)
    ap.add_argument("--state", default=STATE_JSON)
    args = ap.parse_args()

    state = load_state(args.state)
    tmpdir = tempfile.mkdtemp(prefix="basedb-")
    xml_path = os.path.join(tmpdir, "mapping_actor.xml")
    db_path = os.path.join(tmpdir, "actor-mapping.sqlite3")

    if args.offline:
        xml_path = os.path.join(args.offline, "mapping_actor.xml")
        db_path = os.path.join(args.offline, "actor-mapping.sqlite3")
        for p in (xml_path, db_path):
            if not os.path.exists(p):
                log(f"离线模式缺少文件：{p}")
                return 1
        changed = False
        log("离线模式：跳过下载")
    else:
        try:
            xml_path, c1 = fetch_amane(xml_path, state, args.force)
            db_path, c2 = fetch_mdcx(db_path, state, args.force)
            changed = c1 or c2
        except Exception as e:  # noqa: BLE001
            log(f"下载失败：{e}")
            return 1
        if not changed:
            log("两个源都没变，跳过重建")
            return 0

    amane_rows = parse_amane(xml_path)
    mdcx_rows = parse_mdcx(db_path)
    rows = build(amane_rows, mdcx_rows)

    actors = {r[1] for r in rows}
    zh_map = {}
    for _, jp, zh, _ in rows:
        if zh and jp not in zh_map:
            zh_map[jp] = zh

    log(
        f"别名 {len(rows)} 条 / 演员 {len(actors)} / 有中文名 {len(zh_map)} "
        f"（amane {len(amane_rows)} + mdcx {len(mdcx_rows)} 原始行）"
    )

    if args.dry_run:
        log("dry-run：不写文件")
        return 0

    wrote = write_csv(args.out, rows)
    if wrote:
        log(f"写入 {args.out}")
    else:
        log(f"{args.out} 内容无变化，未写入")

    if not args.offline:
        save_state(args.state, state)
        log(f"写入 {args.state}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
