#!/usr/bin/env python3
"""build_base_db.clean_actor_name 的单测。

这个函数决定「谁进演员清单」，改错只有两种代价：漏掉真演员（永久缺人）
或者放进片商/导演（每轮白烧请求）。所以规则逐条钉死。

跑法：python3 scripts/test_build_base_db.py
"""
from __future__ import annotations

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from build_base_db import clean_actor_name  # noqa: E402


class TestCleanActorName(unittest.TestCase):
    def test_keeps_real_actors(self):
        """正常名字一律原样保留——清洗只做减法，不做改写。"""
        for name in (
            "桃乃木かな",
            "三上悠亜",
            "AIKA",
            "Aiko Chino",
            "サラ・ヴァンデラ",
            "Miku Akari (あかり美来)",  # 括号里是别名，不是标签
        ):
            self.assertEqual(clean_actor_name(name), name)

    def test_drops_director(self):
        self.assertEqual(clean_actor_name("AV監督トッキー"), "")
        self.assertEqual(clean_actor_name("監督A"), "")

    def test_drops_gender_mark(self):
        self.assertEqual(clean_actor_name("ようく♂"), "")
        self.assertEqual(clean_actor_name("あや♀"), "")

    def test_drops_amateur_label(self):
        self.assertEqual(clean_actor_name("素人(万引き娘)"), "")
        self.assertEqual(clean_actor_name("素人（円光娘）"), "")

    def test_drops_studios(self):
        for name in ("SOD", "WILD SEVEN", "TECH", "S級素人", "MOODYZ"):
            self.assertEqual(clean_actor_name(name), "", name)
        # 大小写不敏感
        self.assertEqual(clean_actor_name("sod"), "")

    def test_studio_match_is_exact(self):
        """只整名匹配，不做包含匹配：带厂牌字样的真名不能被误杀。"""
        self.assertNotEqual(clean_actor_name("TECHNO"), "")
        self.assertNotEqual(clean_actor_name("SODA みく"), "")

    def test_underscore_becomes_space(self):
        """下划线是拉丁名里空格写错，规范回空格还能继续搜，不要整条丢掉。"""
        self.assertEqual(clean_actor_name("Kristina_Rose"), "Kristina Rose")
        self.assertEqual(clean_actor_name("Lucy_Heart"), "Lucy Heart")

    def test_empty_and_blank(self):
        self.assertEqual(clean_actor_name(""), "")
        self.assertEqual(clean_actor_name("   "), "")
        self.assertEqual(clean_actor_name("\u3000"), "")


if __name__ == "__main__":
    unittest.main(verbosity=2)
