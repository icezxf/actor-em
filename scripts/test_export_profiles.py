#!/usr/bin/env python3
"""export_profiles.split_aliases / pick_mapping 的单测。

改过名的演员（新ありな ← 橋本ありな）靠别名才能找到 mapping 里的旧档案；
但 mapping 同名记录里混着同名新人，必须靠生日/身高校验挡掉，否则串标签。

跑法：python3 scripts/test_export_profiles.py
"""
from __future__ import annotations

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from export_profiles import pick_mapping, split_aliases  # noqa: E402


class TestSplitAliases(unittest.TestCase):
    def test_newline_separated(self):
        """上游 51% 别名用换行分隔，每个都要单独切出来。"""
        got = split_aliases("上乃木まな(デリヘル)\n岩谷志季(撮影会)\n橋本ありな")
        self.assertIn("橋本ありな", got)
        self.assertEqual(len(got), 3)

    def test_pipe_and_punctuation(self):
        got = split_aliases("A|B，C、D/E")
        self.assertEqual(got, ["A", "B", "C", "D", "E"])

    def test_keeps_romaji_space(self):
        """不能按空格切，否则把 Arina Hashimoto 拆成两段。"""
        self.assertEqual(split_aliases("Arina Hashimoto"), ["Arina Hashimoto"])

    def test_empty(self):
        self.assertEqual(split_aliases(""), [])


class TestPickMapping(unittest.TestCase):
    def setUp(self):
        # mapping 里「橋本ありな」有两条：本人(空 bio 被 read_mapping 跳过)和 2005 新人
        self.extra = {
            "橋本ありな": [{
                "tags": ["美少女", "巨乳", "美人"],
                "bio": "身高: 160cm | 罩杯: G | 生涯: 2026~ | 标签: 美少女,巨乳,美人",
                "birth": "2005-10-30",
                "height": 160,
            }],
            "新ありな本人": [{
                "tags": ["最长美腿", "美人"],
                "bio": "身高: 167cm | 标签: 最长美腿,美人",
                "birth": "1996-12-15",
                "height": 167,
            }],
        }

    def test_rejects_wrong_person_via_alias(self):
        """新ありな 通过旧名找到的是别人 → 生日身高都对不上 → 拒绝，不串标签。"""
        got = pick_mapping(self.extra, ["新ありな", "橋本ありな"], "1996-12-15", 167)
        self.assertNotIn("美少女", got.get("tags", []), "不该把 2005 新人的标签贴给本人")

    def test_accepts_same_person_via_alias(self):
        """旧名记录生日身高都对得上 → 采用其标签。"""
        extra = {
            "舊名": [{
                "tags": ["巨乳", "美人"],
                "bio": "身高: 159cm | 标签: 巨乳,美人",
                "birth": "1993-08-16",
                "height": 159,
            }],
        }
        got = pick_mapping(extra, ["现名", "舊名"], "1993-08-16", 159)
        self.assertEqual(got.get("tags"), ["巨乳", "美人"])

    def test_height_tolerance_1cm(self):
        extra = {
            "本人": [{
                "tags": ["标签A"],
                "bio": "身高: 158cm | 标签: 标签A",
                "birth": "",
                "height": 158,
            }],
        }
        got = pick_mapping(extra, ["本人"], "", 159)
        self.assertEqual(got.get("tags"), ["标签A"])

    def test_birth_year_drift_with_same_height(self):
        """生日来源常差 1~2 年；身高一致就算同人，不能误伤。"""
        extra = {
            "MOMONE": [{
                "tags": ["巨乳", "美人"],
                "bio": "身高: 155cm | 标签: 巨乳,美人",
                "birth": "2000-11-30",
                "height": 155,
            }],
        }
        got = pick_mapping(extra, ["MOMONE"], "1999-12-26", 155)
        self.assertEqual(got.get("tags"), ["巨乳", "美人"])

    def test_no_candidates(self):
        self.assertEqual(pick_mapping({}, ["谁"], "1990-01-01", 160), {})


if __name__ == "__main__":
    unittest.main()
