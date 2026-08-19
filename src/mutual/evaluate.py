"""Mutual — evaluate stage：评测 Oracle。

实现 spec/03-oracles.md 与 spec/02-stages.md §11：
- 推荐质量：HR@1/3/5、NDCG@5（复用 AgentRecBench 语义）。
- 互惠公平：envy 计数（复用 FairRec check_envy 语义）。

纯函数，无副作用；只依赖 numpy 与 schemas。
"""

from __future__ import annotations

import numpy as np

from .schemas import EvaluationReport, PrefMatrix


def _rank_of(ground_truth: str, predictions: list[str]) -> float:
    """返回 ground_truth 在 predictions 中的 1-indexed 位置；未命中返回正无穷。"""
    try:
        return float(predictions.index(ground_truth) + 1)
    except ValueError:
        return float("inf")


def _hit_at_k(rank: float, k: int) -> int:
    """rank ≤ K 命中，否则未命中。"""
    return 1 if rank <= k else 0


def _ndcg_at_5(rank: float) -> float:
    """单 ground-truth（IDCG=1）：rank ≤ 5 时 1/log2(rank+1)，否则 0。"""
    if rank <= 5:
        return 1.0 / np.log2(rank + 1)
    return 0.0


def _matches_for(match_prob: np.ndarray, idx: int) -> list[int]:
    """返回 entity idx 被匹配到的对方实体索引列表（match_prob > 0.5）。"""
    row = np.asarray(match_prob[idx, :])
    return [j for j in range(row.shape[0]) if row[j] > 0.5]


def _envy_count(
    pref: np.ndarray,  # [M, N] 行=envier 侧，列=被匹配侧
    match_prob: np.ndarray,  # [M, N]
) -> int:
    """统计一侧的 envy 计数（own-best 语义，qodo #5 措辞澄清）。

    对每个实体 i，取它匹配到的对方集合 J_i；对每个其他实体 i'，若存在
    i' 匹配到的对方 j'，使得 ``pref[i, j'] > max(pref[i, j], j ∈ J_i)``
    （对方拿到的某个选项**严格优于** i 自己**最优**的匹配），则 i 嫉妒
    i'，计数 +1。

    own-best 语义是**显式决定**：与 :func:`mutual.match.check_envy` 保持
    逐位一致（该模块 docstring 同步声明），envy 门禁数值按此口径标定
    （config 门禁 ``total_envy_max=2``）。改语义 = 改 oracle，须走 spec
    变更（spec/05-boundaries.md 前言），不得只改实现。
    """
    n_left = match_prob.shape[0]
    count = 0
    for i in range(n_left):
        own_matches = _matches_for(match_prob, i)
        if not own_matches:
            continue
        own_best = max(pref[i, j] for j in own_matches)
        for i_prime in range(n_left):
            if i_prime == i:
                continue
            other_matches = _matches_for(match_prob, i_prime)
            if any(pref[i, j] > own_best for j in other_matches):
                count += 1
    return count


def evaluate(
    predictions: list[list[str]],
    ground_truth: list[str],
    pref_matrix: PrefMatrix | None = None,
    match_prob: np.ndarray | None = None,
) -> EvaluationReport:
    """计算 HR@1/3/5、NDCG@5（推荐质量）+ envy 计数（互惠公平）。

    Raises:
        ValueError: ``predictions`` 与 ``ground_truth`` 长度不一致（qodo #6：
            非严格 zip 静默截断 + 按 predictions 数做分母会产生错误指标）。
    """
    if len(predictions) != len(ground_truth):
        raise ValueError(
            f"predictions({len(predictions)}) 与 ground_truth({len(ground_truth)}) "
            "长度不一致：评测输入畸形，拒绝计算（qodo #6）"
        )
    total = len(predictions)

    if total == 0:
        return EvaluationReport(
            hr_at_1=0.0,
            hr_at_3=0.0,
            hr_at_5=0.0,
            ndcg_at_5=0.0,
            envy_count_left=0,
            envy_count_right=0,
            total_scenarios=0,
            metadata={"prediction_lengths": []},
        )

    hits_1 = hits_3 = hits_5 = 0
    ndcg = 0.0
    for preds, truth in zip(predictions, ground_truth, strict=False):
        rank = _rank_of(truth, list(preds))
        hits_1 += _hit_at_k(rank, 1)
        hits_3 += _hit_at_k(rank, 3)
        hits_5 += _hit_at_k(rank, 5)
        ndcg += _ndcg_at_5(rank)

    hr1 = hits_1 / total
    hr3 = hits_3 / total
    hr5 = hits_5 / total
    ndcg = ndcg / total

    # Envy 计数：仅当 pref_matrix 和 match_prob 都提供且非空时计算。
    left_envy = 0
    right_envy = 0
    if (
        pref_matrix is not None
        and match_prob is not None
        and getattr(match_prob, "size", 0) > 0
        and np.any(match_prob > 0.5)
    ):
        mp = np.asarray(match_prob)
        left_envy = _envy_count(pref_matrix.pref_left_to_right, mp)
        right_envy = _envy_count(
            pref_matrix.pref_right_to_left, mp.T
        )  # [N, M] 行=right 侧，列=left 侧

    return EvaluationReport(
        hr_at_1=hr1,
        hr_at_3=hr3,
        hr_at_5=hr5,
        ndcg_at_5=ndcg,
        envy_count_left=left_envy,
        envy_count_right=right_envy,
        total_scenarios=total,
        metadata={"prediction_lengths": [len(p) for p in predictions]},
    )
