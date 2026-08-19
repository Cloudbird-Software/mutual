"""Mutual — Golden 回归测试（断言分层，spec/05-boundaries.md §11）。

Phase 1（算法无关不变量，离线 fake_llm + fake_embedder）：
  1. test_basic_cohort       — 4 人 cohort → 6 条 Edge，度分布 {"3": 4}
  2. test_directional_scores — A→B ≠ B→A（分数来自 spec/04-fixtures.md §7.1 分数表）
  3. test_determinism        — 同输入两次运行逐位一致
  4. test_intro_fallback     — LLM 失败时模板话术兜底

Phase 2（NSW 求解器相关，随 match/evaluate 实现而激活）：
  5. test_cohort_envy_free   — cohort 匹配 envy-free
  6. test_evaluate_hr_ndcg   — evaluate() 的 HR@1/3/5、NDCG@5 计算
  7. test_gate_passes        — EvaluationReport.passes_gates 门禁判定

离线优先：不调用真实 LLM，不使用 @pytest.mark.llm。

关于 embedding 阶段：conftest 的 fake_llm 对非打分类 prompt（含 extract）返回话术
模板，导致所有 section 退化为 "Not specified"（spec/05-boundaries.md §1 视为缺失
→ 零向量 → 无相似度）。因此 golden 测试用地 ``golden_stages`` fixture 仅替换
extract/hyde/embed 三个 embedding 相关阶段，产出确定性正相关向量（任意两用户
cosine > 0），保证 select 选出全部 6 对；相似度/选择/打分/兜底匹配/话术/报告
仍走真实实现，fake_llm 承担全部 LLM 依赖阶段（scoring / introduce）。
"""

import json
from pathlib import Path

import numpy as np
import pytest

from mutual import stages
from mutual.match import check_envy, solve_match
from mutual.runners import run_full_match
from mutual.schemas import (
    EmbeddingsBundle,
    EvaluationReport,
    ExtractedSections,
    HydeDescriptors,
    PrefMatrix,
    stable_pair_id,
)

#: 确定性假 embedding 的维度 / 种子（保证跨 run 复现，spec/04-fixtures.md §4）。
_DIM = 8
_SEED = 12345


# ---------------------------------------------------------------------------
# 确定性 embedding 阶段（仅替换 extract/hyde/embed，其余走真实实现）
# ---------------------------------------------------------------------------


def _golden_extract(profiles, config, llm_wrapper, failed_out=None):
    """直接使用画像自带的 sections，保证有内容（不依赖 LLM）。"""
    return [ExtractedSections(id=p.id, sections=dict(p.sections)) for p in profiles]


def _golden_hyde(sections, config, llm_wrapper):
    return {es.id: HydeDescriptors(id=es.id, descriptors={}) for es in sections}


def _golden_embed(sections, hyde, config, existing=None):
    """产出正相关 embedding：公共正向轴保证任意两用户 cosine > 0。"""
    names = sorted({n for es in sections for n in es.sections})
    n = len(sections)
    rng = np.random.RandomState(_SEED)
    base = rng.randn(n, len(names), _DIM)
    base[..., 0] += 5.0
    base /= np.linalg.norm(base, axis=-1, keepdims=True)
    return EmbeddingsBundle(
        user_ids=[es.id for es in sections],
        section_names=names,
        embeddings=base,
        hyde={name: np.zeros((n, 0, _DIM)) for name in names},
        embedding_model="golden-embedder",
        dim=_DIM,
    )


@pytest.fixture
def golden_stages(monkeypatch):
    """替换 embedding 相关阶段为确定性实现；monkeypatch 自动在测试后还原。"""
    monkeypatch.setattr(stages.get_stage("extract"), "run", _golden_extract)
    monkeypatch.setattr(stages.get_stage("hyde"), "run", _golden_hyde)
    monkeypatch.setattr(stages.get_stage("embed"), "run", _golden_embed)


def _run_golden(golden_profiles, config, fake_llm, llm_wrapper=None):
    """按 golden 约定跑一次 full match：fake 按单对查表打分，decouple 预算。"""
    config["budgets"]["n_profiles_to_score_together"] = 1
    return run_full_match(golden_profiles, config, llm_wrapper=llm_wrapper or fake_llm)


class TestPhase1Golden:
    """Phase 1 —— 算法无关不变量（不依赖 NSW 求解器）。"""

    def test_basic_cohort(self, golden_stages, golden_profiles, config, fake_llm):
        """4 人 cohort → 6 条 Edge、度分布 {"3": 4}（由度约束推得）。"""
        match_result = _run_golden(golden_profiles, config, fake_llm)

        assert len(match_result.edges) == 6
        assert match_result.report_data["overview"]["total_edges"] == 6
        assert match_result.report_data["overview"]["total_users"] == 4
        assert match_result.report_data["degree_distribution"] == {"3": 4}

        # 稳定 pair_id 恰好覆盖全部 unordered 对（stable_pair_id 与参数顺序无关）
        expected_pairs = {
            stable_pair_id("alice", "bob"),
            stable_pair_id("alice", "carol"),
            stable_pair_id("alice", "david"),
            stable_pair_id("bob", "carol"),
            stable_pair_id("bob", "david"),
            stable_pair_id("carol", "david"),
        }
        assert {e.pair_id for e in match_result.edges} == expected_pairs

    def test_directional_scores(self, golden_stages, golden_profiles, config, fake_llm):
        """A→B ≠ B→A；具体值来自 fake 分数表（spec/04-fixtures.md §7.1）。"""
        match_result = _run_golden(golden_profiles, config, fake_llm)

        # 带方向性分数的边，双向分数不相等（方向性不盲目对称化，§2）
        for edge in match_result.edges:
            if edge.llm_score_a_to_b is not None and edge.llm_score_b_to_a is not None:
                assert edge.llm_score_a_to_b != edge.llm_score_b_to_a

        # 具体值：alice→bob=0.85，bob→alice=0.90
        alice_bob = next(
            e for e in match_result.edges if e.pair_id == stable_pair_id("alice", "bob")
        )
        assert alice_bob.llm_score_a_to_b == pytest.approx(0.85, abs=1e-3)
        assert alice_bob.llm_score_b_to_a == pytest.approx(0.90, abs=1e-3)

    def test_determinism(self, golden_stages, golden_profiles, config, fake_llm):
        """同输入两次运行 → 相同 edges（同集合、同顺序、同权重）。"""
        match_result1 = _run_golden(golden_profiles, config, fake_llm)
        match_result2 = _run_golden(golden_profiles, config, fake_llm)

        assert match_result1.edges == match_result2.edges

    def test_intro_fallback(self, golden_stages, golden_profiles, config, fake_llm):
        """话术类 LLM 失败时，每条 Edge 回退到模板话术（非空 intro/starter_topics）。"""

        class FailingIntroLLM:
            """打分类正常返回；话术类（prompt 含 "starter"）抛异常。"""

            def __init__(self, base):
                self.base = base
                self.call_count = 0

            def __call__(self, messages, **kwargs):
                self.call_count += 1
                prompt_text = " ".join(str(m.get("content", "")) for m in messages)
                if "a_to_b" in prompt_text:
                    return self.base(messages, **kwargs)  # 打分类
                if "starter" in prompt_text:
                    raise RuntimeError("introduce LLM unavailable")  # 话术类
                return self.base(messages, **kwargs)  # 其他（此处不会被 embed 阶段调用）

            def get_embedding_model(self):
                return self.base.get_embedding_model()

        failing_llm = FailingIntroLLM(fake_llm)
        match_result = _run_golden(golden_profiles, config, fake_llm, llm_wrapper=failing_llm)

        assert match_result.edges
        for edge in match_result.edges:
            assert edge.intro
            assert edge.starter_topics
        # 兜底模板标记（attach_fallback_intro 生成）
        assert "looks like a promising connection" in match_result.edges[0].intro


class TestPhase2Golden:
    """Phase 2 —— 依赖 NSW 求解器 / evaluate 模块（未实现时 skip）。"""

    def test_cohort_envy_free(self, golden_stages, golden_profiles, config, fake_llm):
        """Cohort 匹配为 envy-free（NSW 求解器激活后断言）。"""
        match_result = _run_golden(golden_profiles, config, fake_llm)

        if match_result.envy_report is None:
            pytest.skip("Phase 2: NSW 求解器尚未实现，envy_report 为空")
        assert match_result.envy_report["total_envy"] == 0

    def test_reciprocal_market(self):
        """合成市场 30×20 → 20 条匹配、envy 全 0（fixture test_reciprocal）。"""
        fixture = json.loads(
            (Path(__file__).parent / "golden" / "test_reciprocal" / "market_30x20.json").read_text()
        )
        num_left = fixture["market_config"]["num_left"]
        num_right = fixture["market_config"]["num_right"]
        expected = fixture["expected"]

        # 构造确定性对角偏好市场：匹配后每个实体都拿到自己的最高偏好 → 必然 envy-free。
        left_ids = [f"L{i}" for i in range(num_left)]
        right_ids = [f"R{j}" for j in range(num_right)]
        pref_lr = np.zeros((num_left, num_right), dtype=float)
        pref_rl = np.zeros((num_right, num_left), dtype=float)
        for j in range(num_right):
            # left j 与 right j 互为最高偏好（剩余 10 个 left 无 1:1 伙伴 → 不匹配）
            pref_lr[j, j] = 1.0
            pref_rl[j, j] = 1.0

        pref_matrix = PrefMatrix(
            left_ids=left_ids,
            right_ids=right_ids,
            pref_left_to_right=pref_lr,
            pref_right_to_left=pref_rl,
        )

        edges, match_prob, envy_report = solve_match(
            pref_matrix,
            matching_config={"b_max": 1, "pool_b_max": None},
            blending_config={"embed_weight": 0.5, "llm_weight": 0.5},
        )

        assert len(edges) == expected["total_matches"] == 20
        assert int(match_prob.sum()) == 20
        assert envy_report["left_envy_count"] == expected["envy_count_left"] == 0
        assert envy_report["right_envy_count"] == expected["envy_count_right"] == 0
        # check_envy 独立入口与 solve_match 内嵌报告一致
        assert check_envy(pref_matrix, match_prob)["total_envy"] == 0

    def test_evaluate_hr_ndcg(self):
        """evaluate() 对已知预测/真值计算 HR@1/3/5、NDCG@5。"""
        try:
            from mutual.evaluate import evaluate
        except ImportError:
            pytest.skip("Phase 2: mutual.evaluate 尚未实现")

        predictions = [
            ["a", "b", "c", "d", "e"],  # 真值 "c" 在 rank 3
            ["x", "y", "z"],  # 真值 "x" 在 rank 1
        ]
        ground_truth = ["c", "x"]

        report = evaluate(predictions, ground_truth, None, None)

        # HR@1 = (0 + 1) / 2 = 0.5；HR@3 / HR@5 = 1.0
        assert report.hr_at_1 == pytest.approx(0.5, abs=1e-3)
        assert report.hr_at_3 == pytest.approx(1.0, abs=1e-3)
        assert report.hr_at_5 == pytest.approx(1.0, abs=1e-3)
        # NDCG@5 = (1/log2(4) + 1) / 2 = (0.5 + 1.0) / 2 = 0.75
        assert report.ndcg_at_5 == pytest.approx(0.75, abs=1e-3)

    def test_gate_passes(self):
        """passes_gates 按 config evaluation.gates 判定通过/不通过。"""
        gates = {"hr_at_3_min": 0.6, "ndcg_at_5_min": 0.4, "total_envy_max": 2}

        passing = EvaluationReport(
            hr_at_1=0.8,
            hr_at_3=0.7,
            hr_at_5=0.6,
            ndcg_at_5=0.5,
            envy_count_left=1,
            envy_count_right=0,
        )
        assert passing.passes_gates(gates) is True

        # 各项单独不达标
        low_hr = EvaluationReport(
            hr_at_1=0.5,
            hr_at_3=0.4,
            hr_at_5=0.4,
            ndcg_at_5=0.5,
            envy_count_left=0,
            envy_count_right=0,
        )
        assert low_hr.passes_gates(gates) is False

        low_ndcg = EvaluationReport(
            hr_at_1=0.9,
            hr_at_3=0.8,
            hr_at_5=0.7,
            ndcg_at_5=0.2,
            envy_count_left=0,
            envy_count_right=0,
        )
        assert low_ndcg.passes_gates(gates) is False

        high_envy = EvaluationReport(
            hr_at_1=0.9,
            hr_at_3=0.8,
            hr_at_5=0.7,
            ndcg_at_5=0.6,
            envy_count_left=2,
            envy_count_right=1,
        )
        assert high_envy.passes_gates(gates) is False
