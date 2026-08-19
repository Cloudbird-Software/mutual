"""select stage 单元测试（离线，手造 SimilarityResult）。

覆盖 spec/02-stages.md §5 与 spec/05-boundaries.md §8：
- 贪心轮转 + per-profile cap（max_n_llm_evaluations_per_profile）；
- global cap（max_pair_llm_calls）；
- novelty 排除（excluded_pairs）；
- 只保留正相似度对；
- 平局裁决与确定性（A-7：分数平局取字典序较小 partner）。
"""

import numpy as np
import pytest

from mutual.schemas import CandidatePair, SimilarityResult, stable_pair_id
from mutual.select import select_pairs

IDS = ["a", "b", "c", "d"]


def square_result(scores, ids=None):
    """scores: {user: {other: fused_score}}，dir 与 fused 同值（对称输入）。"""
    ids = ids or IDS
    m = np.zeros((len(ids), len(ids)))
    idx = {u: i for i, u in enumerate(ids)}
    for u, others in scores.items():
        for v, s in others.items():
            m[idx[u], idx[v]] = s
            m[idx[v], idx[u]] = s
    return SimilarityResult(
        source_ids=ids, target_ids=ids, dir_matrix=m.copy(), fused_matrix=m.copy()
    )


def _scores():
    return {
        "a": {"b": 0.9, "c": 0.8, "d": 0.7},
        "b": {"c": 0.85, "d": 0.6},
        "c": {"d": 0.5},
    }


def _pairs(pairs):
    return [(p.user1, p.user2) for p in pairs]


class TestPerProfileCap:
    def test_cap_1_round_robin(self):
        result = square_result(_scores())
        pairs = select_pairs(
            result, {"max_n_llm_evaluations_per_profile": 1, "max_pair_llm_calls": None}
        )
        # 第 1 轮：a→b(0.9)；b/c 已被 a 占用配额？不：b 配额 1 已满；
        # c 的最优 b/a 均满 → c→d(0.5)
        assert _pairs(pairs) == [("a", "b"), ("c", "d")]

    def test_cap_2_greedy_order(self):
        result = square_result(_scores())
        pairs = select_pairs(
            result, {"max_n_llm_evaluations_per_profile": 2, "max_pair_llm_calls": None}
        )
        # 轮 1：a→b(0.9)、b→c(0.85)、c→a(0.8)。
        # 此时 a/b/c 全部配额 2 已满，d 的所有伙伴均 at-cap → d 落选。
        assert _pairs(pairs) == [("a", "b"), ("b", "c"), ("a", "c")]
        counts: dict = {}
        for p in pairs:
            counts[p.user1] = counts.get(p.user1, 0) + 1
            counts[p.user2] = counts.get(p.user2, 0) + 1
        assert counts == {"a": 2, "b": 2, "c": 2}

    def test_no_caps_selects_everything_positive(self):
        result = square_result(_scores())
        pairs = select_pairs(result, {})
        # 无 cap → 全量 6 对
        assert len(pairs) == 6
        assert len({p.pair_id for p in pairs}) == 6


class TestGlobalCap:
    def test_global_cap_limits_total(self):
        result = square_result(_scores())
        pairs = select_pairs(
            result,
            {"max_n_llm_evaluations_per_profile": 4, "max_pair_llm_calls": 3},
        )
        assert len(pairs) == 3
        assert _pairs(pairs) == [("a", "b"), ("b", "c"), ("a", "c")]

    def test_global_cap_zero_returns_empty(self):
        result = square_result(_scores())
        pairs = select_pairs(
            result,
            {"max_n_llm_evaluations_per_profile": 4, "max_pair_llm_calls": 0},
        )
        assert pairs == []


class TestNoveltyExclusion:
    def test_excluded_pair_skipped_next_best_taken(self):
        result = square_result(_scores())
        pairs = select_pairs(
            result,
            {"max_n_llm_evaluations_per_profile": 1, "max_pair_llm_calls": None},
            excluded_pairs={stable_pair_id("a", "b")},
        )
        # a 的最优 a-b 被排除 → a→c(0.8)；b 剩余最优 d(0.6)（c 被 a 占用）
        assert _pairs(pairs) == [("a", "c"), ("b", "d")]

    def test_all_pairs_excluded_returns_empty(self):
        result = square_result({"a": {"b": 0.9}})
        pairs = select_pairs(
            result,
            {},
            excluded_pairs={stable_pair_id("a", "b")},
        )
        assert pairs == []


class TestPositiveFilter:
    def test_non_positive_pairs_never_selected(self):
        scores = {
            "a": {"b": 0.9, "e": -0.4},
            "b": {"e": 0.0},
        }
        result = square_result(scores, ids=["a", "b", "e"])
        pairs = select_pairs(result, {"max_n_llm_evaluations_per_profile": 9})
        flat = {u for p in pairs for u in (p.user1, p.user2)}
        assert "e" not in flat
        assert _pairs(pairs) == [("a", "b")]


class TestRectMode:
    def test_member_and_pool_both_capped(self):
        fused = np.array([[0.9, 0.5], [0.8, 0.4]])
        result = SimilarityResult(
            source_ids=["m1", "m2"],
            target_ids=["p1", "p2"],
            dir_matrix=fused.copy(),
            fused_matrix=fused.copy(),
        )
        pairs = select_pairs(result, {"max_n_llm_evaluations_per_profile": 1})
        # m1→p1(0.9)；p1 配额满 → m2 只能选 p2(0.4)（pool 侧 cap 生效）
        assert _pairs(pairs) == [("m1", "p1"), ("m2", "p2")]

    def test_no_caps_full_bipartite(self):
        fused = np.array([[0.9, 0.5], [0.8, 0.4]])
        result = SimilarityResult(
            source_ids=["m1", "m2"],
            target_ids=["p1", "p2"],
            dir_matrix=fused.copy(),
            fused_matrix=fused.copy(),
        )
        pairs = select_pairs(result, {})
        assert _pairs(pairs) == [("m1", "p1"), ("m2", "p1"), ("m1", "p2"), ("m2", "p2")]

    def test_self_pair_excluded_on_overlap(self):
        fused = np.array([[1.0, 0.6], [0.7, 0.5]])
        result = SimilarityResult(
            source_ids=["m1", "m2"],
            target_ids=["m1", "m2"],
            dir_matrix=fused.copy(),
            fused_matrix=fused.copy(),
        )
        pairs = select_pairs(result, {"max_n_llm_evaluations_per_profile": 9})
        assert all(p.user1 != p.user2 for p in pairs)
        assert ("m1", "m2") in _pairs(pairs)

    def test_uses_fused_not_dir(self):
        # dir 全零、fused 为正 → 仍应产出（选择依据 = fused_matrix，A-8）
        fused = np.array([[0.9, 0.3]])
        result = SimilarityResult(
            source_ids=["m1"],
            target_ids=["p1", "p2"],
            dir_matrix=np.zeros_like(fused),
            fused_matrix=fused,
        )
        pairs = select_pairs(result, {"max_n_llm_evaluations_per_profile": 1})
        assert _pairs(pairs) == [("m1", "p1")]


class TestCandidatePairContract:
    def test_user_order_and_pair_id_normalized(self):
        fused = np.array([[0.9, 0.3]])
        result = SimilarityResult(
            source_ids=["zeta"],
            target_ids=["alpha", "beta"],
            dir_matrix=fused.copy(),
            fused_matrix=fused.copy(),
        )
        (pair,) = select_pairs(result, {"max_n_llm_evaluations_per_profile": 1})
        assert isinstance(pair, CandidatePair)
        assert pair.user1 == "alpha" < pair.user2 == "zeta"
        assert pair.pair_id == stable_pair_id("alpha", "zeta") == "alpha__zeta"
        assert pair.similarity_score == pytest.approx(0.9)

    def test_tie_breaks_to_lexicographically_smaller_partner(self):
        fused = np.array([[0.7, 0.7]])  # p2 列在前但同分
        result = SimilarityResult(
            source_ids=["m1"],
            target_ids=["p2", "p1"],
            dir_matrix=fused.copy(),
            fused_matrix=fused.copy(),
        )
        (pair,) = select_pairs(result, {"max_n_llm_evaluations_per_profile": 1})
        assert pair.user2 == "p1"


class TestDeterminism:
    def test_same_input_same_output(self):
        result = square_result(_scores())
        budgets = {"max_n_llm_evaluations_per_profile": 2, "max_pair_llm_calls": 5}
        assert select_pairs(result, budgets) == select_pairs(result, budgets)

    def test_default_budgets_from_config(self, config):
        """冒烟：用 config/default.yaml 的真实预算跑通（不硬编码断言具体值）。"""
        result = square_result(_scores())
        pairs = select_pairs(result, config["budgets"], excluded_pairs=set())
        assert len(pairs) == 6  # 4 人全量 6 对，远低于默认 cap
