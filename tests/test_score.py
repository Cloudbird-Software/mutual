"""score 阶段测试：双向打分、预算、unscored_out、归一化、PrefMatrix。

fake_llm 契约见 spec/04-fixtures.md §7.1：打分类 prompt 必含 "a_to_b" 标记，
fake 按字典序最小两个 cohort id 查表返回。
"""

import copy

import numpy as np
import pytest

from mutual.config import resolve_prompt_templates
from mutual.schemas import CandidatePair, ExtractedSections, PairScore
from mutual.score import (
    build_pref_matrix,
    create_sections_dict,
    prepare_normalized_scores,
    score_pairs_with_llm,
)


@pytest.fixture
def scoring_template(config):
    return resolve_prompt_templates(config)["scoring"]


@pytest.fixture
def cohort_sections():
    return [
        ExtractedSections(id=uid, sections={"skills": f"{uid} skills", "needs": f"{uid} needs"})
        for uid in ("alice", "bob", "carol", "david")
    ]


def _pair(u1: str, u2: str, score: float = 0.5) -> CandidatePair:
    return CandidatePair.create(u1, u2, score)


def _score(pairs, fake_llm, config, sections, unscored=None, **cfg_overrides):
    cfg = copy.deepcopy(config)
    for dotted, value in cfg_overrides.items():
        node = cfg
        keys = dotted.split(".")
        for k in keys[:-1]:
            node = node.setdefault(k, {})
        node[keys[-1]] = value
    return score_pairs_with_llm(
        pairs,
        create_sections_dict(sections),
        instruction=cfg["recipe"]["instruction"],
        prompt_template=resolve_prompt_templates(cfg)["scoring"],
        llm_wrapper=fake_llm,
        config=cfg,
        unscored_out=unscored,
    )


class TestCreateSectionsDict:
    def test_maps_by_user_id(self, cohort_sections):
        d = create_sections_dict(cohort_sections)
        assert set(d) == {"alice", "bob", "carol", "david"}
        assert d["alice"]["skills"] == "alice skills"

    def test_empty(self):
        assert create_sections_dict([]) == {}


class TestBidirectionalScoring:
    def test_directional_scores_from_fake_table(self, fake_llm, config, cohort_sections):
        scores = _score([_pair("alice", "bob")], fake_llm, config, cohort_sections)
        ps = scores["alice__bob"]
        assert ps.llm_score_a_to_b == pytest.approx(0.85)
        assert ps.llm_score_b_to_a == pytest.approx(0.90)

    def test_asymmetry_preserved(self, fake_llm, config, cohort_sections):
        """方向性不盲目对称化（spec/05-boundaries.md §2）。"""
        scores = _score([_pair("carol", "david")], fake_llm, config, cohort_sections)
        ps = scores["carol__david"]
        assert ps.llm_score_a_to_b == pytest.approx(0.35)
        assert ps.llm_score_b_to_a == pytest.approx(0.65)
        assert ps.llm_score_a_to_b != ps.llm_score_b_to_a
        # 融合分 = 双向算术平均（沉默 S3）
        assert ps.llm_score == pytest.approx((0.35 + 0.65) / 2)

    def test_order_invariant_pair_id(self, fake_llm, config, cohort_sections):
        s1 = _score([_pair("bob", "alice")], fake_llm, config, cohort_sections)
        s2 = _score([_pair("alice", "bob")], fake_llm, config, cohort_sections)
        assert s1["alice__bob"].llm_score_a_to_b == s2["alice__bob"].llm_score_a_to_b

    def test_embed_score_kept(self, fake_llm, config, cohort_sections):
        scores = _score([_pair("alice", "bob", 0.42)], fake_llm, config, cohort_sections)
        assert scores["alice__bob"].embed_score == pytest.approx(0.42)

    def test_default_fallback_for_unknown_users(self, fake_llm, config, cohort_sections):
        sections = cohort_sections + [
            ExtractedSections(id="zoe", sections={"skills": "z"}),
            ExtractedSections(id="yan", sections={"skills": "y"}),
        ]
        scores = _score([_pair("zoe", "yan")], fake_llm, config, sections)
        ps = scores["yan__zoe"]  # stable_pair_id 按字典序
        assert ps.llm_score_a_to_b == pytest.approx(0.5)
        assert ps.llm_score_b_to_a == pytest.approx(0.5)

    def test_missing_user_sections_render_not_specified(self, fake_llm, config, cohort_sections):
        """sections_dict 缺失的用户以 "Not specified" 兜底，不崩。"""
        scores = _score([_pair("alice", "bob")], fake_llm, config, cohort_sections[:1])
        assert scores["alice__bob"].llm_score_a_to_b == pytest.approx(0.85)

    def test_batching_reduces_calls(self, fake_llm, config, cohort_sections):
        pairs = [_pair("alice", "bob"), _pair("carol", "david")]
        _score(
            pairs, fake_llm, config, cohort_sections, **{"budgets.n_profiles_to_score_together": 2}
        )
        assert fake_llm.call_count == 1
        _score(
            pairs, fake_llm, config, cohort_sections, **{"budgets.n_profiles_to_score_together": 1}
        )
        assert fake_llm.call_count == 3  # 1 + 2

    def test_multi_pair_batch_with_single_object_response_all_unscored(
        self, fake_llm, config, cohort_sections
    ):
        """多对 batch 只接受 JSON 数组；fake 返回单对象 → 整批未打分但保留（§3）。"""
        unscored = []
        pairs = [_pair("alice", "bob"), _pair("carol", "david")]
        scores = _score(
            pairs,
            fake_llm,
            config,
            cohort_sections,
            unscored,
            **{"budgets.n_profiles_to_score_together": 2},
        )
        assert scores["alice__bob"].llm_score is None
        assert scores["carol__david"].llm_score is None
        assert len(unscored) == 2

    def test_array_response_parsed_per_index(self, config, cohort_sections):
        """多对 batch 收到 JSON 数组时按序对齐。"""
        calls = []

        def fake_llm_arr(messages, **kwargs):
            calls.append(1)
            return '[{"a_to_b": 0.1, "b_to_a": 0.2}, {"a_to_b": 0.3, "b_to_a": 0.4}]'

        pairs = [_pair("alice", "bob"), _pair("carol", "david")]
        scores = _score(
            pairs,
            fake_llm_arr,
            config,
            cohort_sections,
            **{"budgets.n_profiles_to_score_together": 2},
        )
        assert scores["alice__bob"].llm_score_a_to_b == pytest.approx(0.1)
        assert scores["alice__bob"].llm_score_b_to_a == pytest.approx(0.2)
        assert scores["carol__david"].llm_score_a_to_b == pytest.approx(0.3)


class TestUnscoredOut:
    def test_all_pairs_kept_in_result(self, fake_llm, config, cohort_sections):
        """未打分候选不丢弃：仍出现在返回 dict 中，llm 字段为 None（§3）。"""
        unscored = []
        pairs = [_pair("alice", "bob"), _pair("carol", "david")]
        scores = _score(
            pairs,
            fake_llm,
            config,
            cohort_sections,
            unscored,
            **{"budgets.max_pair_llm_calls": 0},
        )
        assert set(scores) == {"alice__bob", "carol__david"}
        assert all(ps.llm_score is None for ps in scores.values())
        assert all(ps.embed_score is not None for ps in scores.values())
        assert {p.pair_id for p in unscored} == {"alice__bob", "carol__david"}

    def test_llm_exception_marks_unscored(self, config, cohort_sections):
        class BoomLLM:
            def __call__(self, messages, **kwargs):
                raise RuntimeError("llm down")

        unscored = []
        scores = _score([_pair("alice", "bob")], BoomLLM(), config, cohort_sections, unscored)
        assert scores["alice__bob"].llm_score is None
        assert len(unscored) == 1

    def test_garbage_response_marks_unscored(self, config, cohort_sections):
        class GarbageLLM:
            def __call__(self, messages, **kwargs):
                return "not json at all"

        unscored = []
        scores = _score([_pair("alice", "bob")], GarbageLLM(), config, cohort_sections, unscored)
        assert scores["alice__bob"].llm_score is None
        assert len(unscored) == 1


class TestBudgets:
    def test_per_profile_cap(self, fake_llm, config, cohort_sections):
        """max_n_llm_evaluations_per_profile=1：alice 只打第一对。"""
        unscored = []
        pairs = [
            _pair("alice", "bob"),
            _pair("alice", "carol"),
            _pair("alice", "david"),
            _pair("bob", "carol"),
        ]
        scores = _score(
            pairs,
            fake_llm,
            config,
            cohort_sections,
            unscored,
            **{"budgets.max_n_llm_evaluations_per_profile": 1},
        )
        assert scores["alice__bob"].llm_score is not None
        assert scores["alice__carol"].llm_score is None
        assert scores["alice__david"].llm_score is None
        assert scores["bob__carol"].llm_score is None  # bob 已耗尽
        assert {p.pair_id for p in unscored} == {
            "alice__carol",
            "alice__david",
            "bob__carol",
        }

    def test_global_call_cap(self, fake_llm, config, cohort_sections):
        """max_pair_llm_calls=1：只发 1 次调用，其余未打分。"""
        unscored = []
        pairs = [_pair("alice", "bob"), _pair("alice", "carol"), _pair("bob", "carol")]
        scores = _score(
            pairs,
            fake_llm,
            config,
            cohort_sections,
            unscored,
            **{
                "budgets.max_pair_llm_calls": 1,
                "budgets.n_profiles_to_score_together": 1,
            },
        )
        assert fake_llm.call_count == 1
        assert scores["alice__bob"].llm_score is not None
        assert {p.pair_id for p in unscored} == {"alice__carol", "bob__carol"}

    def test_budget_counts_attempted_calls(self, fake_llm, config, cohort_sections):
        """解析失败的调用同样消耗全局预算。"""

        class HalfBrokenLLM:
            def __init__(self):
                self.calls = 0

            def __call__(self, messages, **kwargs):
                self.calls += 1
                return "garbage" if self.calls == 1 else '{"a_to_b": 0.7, "b_to_a": 0.8}'

        llm = HalfBrokenLLM()
        pairs = [_pair("alice", "bob"), _pair("carol", "david")]
        scores = _score(
            pairs,
            llm,
            config,
            cohort_sections,
            **{
                "budgets.max_pair_llm_calls": 2,
                "budgets.n_profiles_to_score_together": 1,
            },
        )
        assert scores["alice__bob"].llm_score is None  # 第一次调用坏了
        assert scores["carol__david"].llm_score_a_to_b == pytest.approx(0.7)

    def test_dedupe_pairs(self, fake_llm, config, cohort_sections):
        pairs = [_pair("alice", "bob", 0.6), _pair("alice", "bob", 0.9)]
        scores = _score(pairs, fake_llm, config, cohort_sections)
        assert len(scores) == 1
        assert scores["alice__bob"].embed_score == pytest.approx(0.6)  # 保留首个


class TestPrepareNormalizedScores:
    def test_batch_stats_when_no_reference(self):
        scores = {
            "a__b": PairScore("a__b", "a", "b", embed_score=0.2, llm_score=0.2),
            "c__d": PairScore("c__d", "c", "d", embed_score=0.4, llm_score=0.6),
            "e__f": PairScore("e__f", "e", "f", embed_score=0.6),
        }
        out = prepare_normalized_scores(scores)
        assert out["a__b"].embed_score_normalized == pytest.approx(0.0)
        assert out["c__d"].embed_score_normalized == pytest.approx(0.5)
        assert out["e__f"].embed_score_normalized == pytest.approx(1.0)
        # llm 批次统计：min=0.2（a__b）max=0.6（c__d）
        assert out["a__b"].llm_score_normalized == pytest.approx(0.0)
        assert out["c__d"].llm_score_normalized == pytest.approx(1.0)
        assert out["e__f"].llm_score_normalized is None  # 未打分保持 None

    def test_reference_drives_embed_normalization(self):
        """reference 分布驱动跨批次稳定归一化：批次内统计被忽略。"""
        scores = {
            "a__b": PairScore("a__b", "a", "b", embed_score=0.30, llm_score=0.2),
            "c__d": PairScore("c__d", "c", "d", embed_score=0.35, llm_score=0.6),
        }
        reference = np.array([0.0, 1.0])
        out = prepare_normalized_scores(scores, reference=reference)
        assert out["a__b"].embed_score_normalized == pytest.approx(0.30)
        assert out["c__d"].embed_score_normalized == pytest.approx(0.35)
        # llm 仍用批次统计（reference 单数组解释为 embed 参考分布，沉默 S2）
        assert out["a__b"].llm_score_normalized == pytest.approx(0.0)
        assert out["c__d"].llm_score_normalized == pytest.approx(1.0)

    def test_reference_clips_out_of_range(self):
        scores = {"a__b": PairScore("a__b", "a", "b", embed_score=1.7)}
        out = prepare_normalized_scores(scores, reference=np.array([0.0, 1.0]))
        assert out["a__b"].embed_score_normalized == pytest.approx(1.0)

    def test_degenerate_returns_neutral(self):
        scores = {"a__b": PairScore("a__b", "a", "b", embed_score=0.5)}
        out = prepare_normalized_scores(scores)
        assert out["a__b"].embed_score_normalized == pytest.approx(0.5)

    def test_input_not_mutated(self):
        scores = {"a__b": PairScore("a__b", "a", "b", embed_score=0.5, llm_score=0.5)}
        out = prepare_normalized_scores(scores)
        assert scores["a__b"].embed_score_normalized is None
        assert out["a__b"].embed_score_normalized is not None

    def test_empty(self):
        assert prepare_normalized_scores({}) == {}


class TestBuildPrefMatrix:
    def test_directional_fill(self):
        """a_to_b → pref_left_to_right[i,j]；b_to_a → pref_right_to_left[j,i]。"""
        scores = {
            "alice__bob": PairScore(
                "alice__bob",
                "alice",
                "bob",
                embed_score=0.5,
                llm_score_a_to_b=0.85,
                llm_score_b_to_a=0.90,
            ),
        }
        pm = build_pref_matrix(scores, ["alice", "bob"])
        i, j = pm.left_ids.index("alice"), pm.right_ids.index("bob")
        assert pm.pref_left_to_right[i, j] == pytest.approx(0.85)
        assert pm.pref_right_to_left[j, i] == pytest.approx(0.90)
        # 互补单元格（沉默 S7）：同一无序对的另一方向
        assert pm.pref_left_to_right[j, i] == pytest.approx(0.90)
        assert pm.pref_right_to_left[i, j] == pytest.approx(0.85)

    def test_embed_fallback_for_missing_llm(self):
        scores = {
            "alice__bob": PairScore("alice__bob", "alice", "bob", embed_score=0.42),
        }
        pm = build_pref_matrix(scores, ["alice", "bob"])
        i, j = pm.left_ids.index("alice"), pm.right_ids.index("bob")
        assert pm.pref_left_to_right[i, j] == pytest.approx(0.42)
        assert pm.pref_right_to_left[j, i] == pytest.approx(0.42)

    def test_missing_pair_is_zero(self):
        scores = {
            "alice__bob": PairScore(
                "alice__bob",
                "alice",
                "bob",
                embed_score=0.5,
                llm_score_a_to_b=0.8,
                llm_score_b_to_a=0.9,
            ),
        }
        pm = build_pref_matrix(scores, ["alice", "bob", "carol"])
        k = pm.left_ids.index("carol")
        assert pm.pref_left_to_right[k].max() == 0.0
        assert pm.pref_right_to_left[:, k].max() == 0.0

    def test_diagonal_zero_and_shape(self):
        scores = {
            "alice__bob": PairScore(
                "alice__bob",
                "alice",
                "bob",
                embed_score=0.5,
                llm_score_a_to_b=0.8,
                llm_score_b_to_a=0.9,
            ),
        }
        pm = build_pref_matrix(scores, ["alice", "bob", "carol"])
        assert pm.pref_left_to_right.shape == (3, 3)
        assert pm.pref_right_to_left.shape == (3, 3)
        assert pm.left_ids == pm.right_ids
        assert np.all(np.diag(pm.pref_left_to_right) == 0.0)
        assert np.all(np.diag(pm.pref_right_to_left) == 0.0)

    def test_end_to_end_from_score_pairs(self, fake_llm, config, cohort_sections):
        pairs = [_pair("alice", "bob"), _pair("carol", "david", 0.3)]
        scores = _score(
            pairs,
            fake_llm,
            config,
            cohort_sections,
            **{"budgets.n_profiles_to_score_together": 1},
        )
        pm = build_pref_matrix(scores, ["alice", "bob", "carol", "david"])
        a = pm.left_ids.index("alice")
        b = pm.right_ids.index("bob")
        assert pm.pref_left_to_right[a, b] == pytest.approx(0.85)
        c = pm.left_ids.index("carol")
        d = pm.right_ids.index("david")
        assert pm.pref_left_to_right[c, d] == pytest.approx(0.35)
        assert pm.pref_right_to_left[d, c] == pytest.approx(0.65)


class TestPurity:
    def test_no_mutation_of_inputs(self, fake_llm, config, cohort_sections, scoring_template):
        pairs = [_pair("alice", "bob", 0.5)]
        sections_dict = create_sections_dict(cohort_sections)
        snapshot = {k: dict(v) for k, v in sections_dict.items()}
        score_pairs_with_llm(
            pairs,
            sections_dict,
            instruction="instr",
            prompt_template=scoring_template,
            llm_wrapper=fake_llm,
            config=config,
        )
        assert sections_dict == snapshot
