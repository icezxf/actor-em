#!/usr/bin/env python3
"""生成演员库站点的 favicon（纯 Pillow，无外部依赖）。

设计（与站点玫瑰红 #e11d48 配色一致）：
  - 圆角方块底色
  - 白色「人像剪影」：头（圆）+ 肩（圆弧），代表演员
  - 左下角一张小名片，代表「档案库」

输出：favicon.ico（16/32/48/64）+ apple-touch-icon.png + PWA 192/512。
用法：python3 scripts/make_favicon.py
"""
from __future__ import annotations

import os
from PIL import Image, ImageDraw

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, "site_src", "assets")
os.makedirs(OUT, exist_ok=True)

ACCENT = "#e11d48"
WHITE = "#ffffff"
S = 512  # 超采样画布


def draw_icon(size: int) -> Image.Image:
    img = Image.new("RGBA", (S, S), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)
    d.rounded_rectangle([0, 0, S - 1, S - 1], radius=int(S * 0.22), fill=ACCENT)

    # 肩：大椭圆，只保留下半（用一个底部椭圆，其上沿自然成弧）
    d.ellipse([int(S * 0.18), int(S * 0.48), int(S * 0.82), int(S * 1.10)], fill=WHITE)
    # 头：画在肩之上
    hr = int(S * 0.115)
    cx, hy = int(S * 0.50), int(S * 0.34)
    d.ellipse([cx - hr, hy - hr, cx + hr, hy + hr], fill=WHITE)

    # 左下角名片：描边 + 两行文字线
    card = [int(S * 0.075), int(S * 0.625), int(S * 0.325), int(S * 0.835)]
    w = max(3, S // 90)
    d.rounded_rectangle(card, radius=int(S * 0.035), outline=WHITE, width=w)
    y1, y2 = card[1] + int(S * 0.075), card[1] + int(S * 0.13)
    d.line([card[0] + int(S * 0.045), y1, card[2] - int(S * 0.045), y1], fill=WHITE, width=w)
    d.line([card[0] + int(S * 0.045), y2, card[2] - int(S * 0.11), y2], fill=WHITE, width=max(2, S // 120))

    return img.resize((size, size), Image.Resampling.LANCZOS)


def main():
    sizes = [16, 32, 48, 64]
    # Pillow 13：ICO 用一张大图 + sizes 尺寸元组列表，插件自己缩放
    master = draw_icon(256)
    master.save(os.path.join(OUT, "favicon.ico"), format="ICO",
                sizes=[(s, s) for s in sizes])
    draw_icon(180).save(os.path.join(OUT, "apple-touch-icon.png"), optimize=True)
    draw_icon(192).save(os.path.join(OUT, "icon-192.png"), optimize=True)
    draw_icon(512).save(os.path.join(OUT, "icon-512.png"), optimize=True)
    for name in ("favicon.ico", "apple-touch-icon.png", "icon-192.png", "icon-512.png"):
        p = os.path.join(OUT, name)
        print(f"{name}: {os.path.getsize(p)} bytes")


if __name__ == "__main__":
    main()
