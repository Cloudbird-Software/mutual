"""Oracle 测试：HR@K / NDCG@5 / Envy 计算（spec/03-oracles.md）。

全部离线，不依赖真实 LLM。evaluate() 的确定性契约见 spec/03-oracles.md。
"""

import numpy as np
import pytest

from mutual.evaluate import evaluate
from mutual.schemas import EvaluationReport, PrefMatrix

# ---------------------------------------------------------------------------
# HR@K
# ---------------------------------------------------------------------------


def test_hr_at_n():
    predictions = [["b", "c", "d"], ["c", "d", "e"], ["a", "b", "c"]]
    ground_truth = ["b", "e", "x"]

    report = evaluate(predictions, ground_truth)

    # scenario 0 命中（b 在 top-1）；scenario 1 / 2 未命中
    assert report.hr_at_1 == pytest.approx(1 / 3)
    # scenario 0（b）与 scenario 1（e）命中，scenario 2 未命中
    assert report.hr_at_3 == pytest.approx(2 / 3)
    assert report.hr_at_5 == pytest.approx(2 / 3)


# ---------------------------------------------------------------------------
# NDCG@5
# ---------------------------------------------------------------------------


def test_ndcg_at_5():
    # ground_truth 在 rank 2：NDCG@5 = 1/log2(2+1) = 1/log2(3)
    predictions = [["a", "b", "c", "d", "e"]]
    report = evaluate(predictions, ["b"])
    assert report.ndcg_at_5 == pytest.approx(1 / np.log2(3), abs=1e-4)
    assert report.ndcg_at_5 == pytest.approx(0.6309, abs=1e-4)

    # 同样 ground_truth 在 rank 2（b 之后）
    report2 = evaluate([["b", "a", "c"]], ["a"])
    assert report2.ndcg_at_5 == pytest.approx(0.6309, abs=1e-4)

    # ground_truth 不在 top-5 → NDCG = 0.0
    report3 = evaluate([["b", "a", "c"]], ["z"])
    assert report3.ndcg_at_5 == 0.0


# ---------------------------------------------------------------------------
# Envy-free matching
# ---------------------------------------------------------------------------


def test_envy_free():
    left_ids = ["L0", "L1", "L2"]
    right_ids = ["R0", "R1", "R2"]

    # 每个 left 都最偏好自己匹配到的 right（对角线最高）
    pref_left_to_right = np.array(
        [
            [1.0, 0.0, 0.0],
            [0.0, 1.0, 0.0],
            [0.0, 0.0, 1.0],
        ]
    )
    pref_right_to_left = np.array(
        [
            [1.0, 0.0, 0.0],
            [0.0, 1.0, 0.0],
            [0.0, 0.0, 1.0],
        ]
    )
    pref_matrix = PrefMatrix(
        left_ids=left_ids,
        right_ids=right_ids,
        pref_left_to_right=pref_left_to_right,
        pref_right_to_left=pref_right_to_left,
    )

    # 完美匹配：L0→R0, L1→R1, L2→R2
    match_prob = np.eye(3)

    report = evaluate(
        predictions=[["a"], ["a"], ["a"]],
        ground_truth=["a", "a", "a"],
        pref_matrix=pref_matrix,
        match_prob=match_prob,
    )
    assert report.envy_count_left == 0
    assert report.envy_count_right == 0


# ---------------------------------------------------------------------------
# Matching with envy
# ---------------------------------------------------------------------------


def test_envy_exists():
    left_ids = ["L0", "L1", "L2"]
    right_ids = ["R0", "R1", "R2"]

    # L0 强烈偏好 R1（1.0）但被匹配给 R0（0.2）→ L0 会嫉妒得到 R1 的 L1
    pref_left_to_right = np.array(
        [
            [0.2, 1.0, 0.0],
            [0.0, 1.0, 0.0],
            [0.0, 0.0, 1.0],
        ]
    )
    pref_right_to_left = np.array(
        [
            [1.0, 0.0, 0.0],
            [0.0, 1.0, 0.0],
            [0.0, 0.0, 1.0],
        ]
    )
    pref_matrix = PrefMatrix(
        left_ids=left_ids,
        right_ids=right_ids,
        pref_left_to_right=pref_left_to_right,
        pref_right_to_left=pref_right_to_left,
    )

    # 完美匹配：L0→R0, L1→R1, L2→R2
    match_prob = np.eye(3)

    report = evaluate(
        predictions=[["a"], ["a"], ["a"]],
        ground_truth=["a", "a", "a"],
        pref_matrix=pref_matrix,
        match_prob=match_prob,
    )
    assert report.envy_count_left > 0


# ---------------------------------------------------------------------------
# Gate checking
# ---------------------------------------------------------------------------

_GATES = {"hr_at_3_min": 0.6, "ndcg_at_5_min": 0.4, "total_envy_max": 2}


def test_gate_passes():
    report = EvaluationReport(
        hr_at_1=0.6,
        hr_at_3=0.6,
        hr_at_5=0.6,
        ndcg_at_5=0.4,
        envy_count_left=0,
        envy_count_right=0,
    )
    assert report.passes_gates(_GATES) is True


def test_gate_fails_hr_too_low():
    report = EvaluationReport(
        hr_at_1=0.5,
        hr_at_3=0.5,
        hr_at_5=0.5,
        ndcg_at_5=0.4,
        envy_count_left=0,
        envy_count_right=0,
    )
    assert report.passes_gates(_GATES) is False


def test_gate_fails_ndcg_too_low():
    report = EvaluationReport(
        hr_at_1=0.7,
        hr_at_3=0.7,
        hr_at_5=0.7,
        ndcg_at_5=0.3,
        envy_count_left=0,
        envy_count_right=0,
    )
    assert report.passes_gates(_GATES) is False


def test_gate_fails_envy_too_high():
    report = EvaluationReport(
        hr_at_1=0.8,
        hr_at_3=0.8,
        hr_at_5=0.8,
        ndcg_at_5=0.5,
        envy_count_left=3,
        envy_count_right=0,
    )
    assert report.passes_gates(_GATES) is False


# ---------------------------------------------------------------------------
# Edge cases
# ---------------------------------------------------------------------------


def test_hr_ndcg_with_empty_predictions():
    # 空 predictions 列表 → 所有指标为 0.0
    report = evaluate(predictions=[], ground_truth=[])
    assert report.hr_at_1 == 0.0
    assert report.hr_at_3 == 0.0
    assert report.hr_at_5 == 0.0
    assert report.ndcg_at_5 == 0.0

    # 空 inner list → HR miss，NDCG = 0
    report2 = evaluate(predictions=[[]], ground_truth=["a"])
    assert report2.hr_at_1 == 0.0
    assert report2.hr_at_3 == 0.0
    assert report2.ndcg_at_5 == 0.0


def test_hr_ndcg_mixed_rank_positions():
    predictions = [["a", "b", "c"], ["a", "b", "c"], ["a", "b", "c"]]
    ground_truth = ["a", "b", "x"]

    report = evaluate(predictions, ground_truth)

    # scenario 0（a, rank1）命中 HR@1；scenario 1（b, rank2）不命中 HR@1
    assert report.hr_at_1 == pytest.approx(1 / 3)
    # scenario 0 / 1 均在 top-3 命中
    assert report.hr_at_3 == pytest.approx(2 / 3)

    # NDCG@5 = (1/log2(2) + 1/log2(3) + 0) / 3
    expected_ndcg = (1.0 + 1 / np.log2(3) + 0.0) / 3
    assert report.ndcg_at_5 == pytest.approx(expected_ndcg, abs=1e-4)
    assert report.ndcg_at_5 == pytest.approx(0.5436, abs=1e-4)
