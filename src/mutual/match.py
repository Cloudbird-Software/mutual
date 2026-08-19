"""Mutual — NSW（Nash Social Welfare）匹配求解 + envy 公平性检查。

对应 spec/02-stages.md §8、docs/engineering-plan.md §4.2（Phase 2）。

本模块是**无外部依赖**的独立求解器：只用 numpy（不依赖 FairRec/cvxpy/torch）。
输入是 :class:`~mutual.schemas.PrefMatrix`（双向偏好矩阵），输出为
``(list[Edge], match_prob, envy_report)``。

算法：按 NSW 分数（双向偏好几何平均 ``sqrt(pref_lr[i,j] * pref_rl[j,i])``）
降序的确定性贪心 b-matching（平局取 ``(i, j)`` 字典序）。

边界（spec/05-boundaries.md）：
- §7 度约束 ``b_max`` 绑定 member（左）侧；``pool_b_max`` 可选绑定 pool（右）侧。
  同集（left == right，如 cohort/full 模式）退化为无向图：``b_max`` 对称约束
  双方；``match_prob`` 对称存储。
- §3 未打分候选保留 embedding 权重（在 score 阶段已归一化，此处用偏好矩阵兜底）。

``b_min``（每人最少匹配数，qodo #9）：下界强制需要最小度 b-matching，
可行性依赖候选图密度（稀疏市场/novelty 排除后可能无解）。本求解器不静默
假装满足——贪心后做**显式可行性检查**，报告 ``b_min_violations``
（度数不足的 member id 列表）与 ``b_min_satisfied``，由调用方决定
（继续运行 / 报警 / 门禁阻断）。上界贪心保证不超 ``b_max``；下界
不可满足时**显式报告**，绝不静默吞掉。

说明：match 阶段只见 ``PrefMatrix``（无独立可分离的 embed/llm 分），因此
边的 ``final_weight`` 取 **NSW 分数**，``embed_score``/``llm_score`` 字段
以该分数重建（blending 公式在 embed==llm==nsw 时退化为 nsw）。
归一化由 :func:`mutual.score.prepare_normalized_scores` 在 score 阶段完成，
``reference_scores`` 在此仅透传。
"""

from __future__ import annotations

from math import sqrt
from typing import Any, List, Set, Tuple

import numpy as np

from .schemas import Edge, PrefMatrix, stable_pair_id


def solve_match(
    pref_matrix: PrefMatrix,
    matching_config: dict,
    blending_config: dict,
    reference_scores: np.ndarray | None = None,
) -> tuple[list[Edge], np.ndarray, dict]:
    """NSW 匹配求解 + envy 检查。

    Args:
        pref_matrix: 双向偏好矩阵（left→right 与 right→left）。
        matching_config: 度约束（``b_max``/``pool_b_max`` 上界贪心强制；
            ``b_min`` 下界显式可行性检查——不可满足时报告
            ``b_min_violations``，不静默，见模块 docstring）。
        blending_config: 混合权重（``embed_weight``/``llm_weight``）。
        reference_scores: 可选参考分数线；由 score 阶段消费，此处透传。

    Returns:
        ``(list[Edge], match_prob, envy_report)``：
        - 匹配边（按 ``(-final_weight, pair_id)`` 排序）；
        - 匹配概率矩阵（确定性匹配 → 0/1，shape ``[M, N]``；
          同集（无向）匹配对称存储 ``prob[i,j] == prob[j,i]``）；
        - envy 报告（``left_envy_count``/``right_envy_count``/``total_envy``/
          ``left``/``right``）+ ``b_min`` 可行性字段
          （``b_min``/``b_min_violations``/``b_min_satisfied``）。
    """
    M = len(pref_matrix.left_ids)
    N = len(pref_matrix.right_ids)
    b_min = max(0, _to_int(matching_config.get("b_min"), 0))
    if M == 0 or N == 0:
        report = _empty_envy()
        _attach_b_min_report(report, pref_matrix, np.zeros((M, N), dtype=int), b_min)
        return [], np.zeros((M, N), dtype=int), report

    b_max = _to_int(matching_config.get("b_max"), 0)
    pool_b_max = matching_config.get("pool_b_max")
    if pool_b_max is not None:
        pool_b_max = _to_int(pool_b_max, 0)

    w_embed = _to_float(blending_config.get("embed_weight"), 0.5)
    w_llm = _to_float(blending_config.get("llm_weight"), 0.5)

    same_set = list(pref_matrix.left_ids) == list(pref_matrix.right_ids)

    match_prob = np.zeros((M, N), dtype=int)
    matched_pairs: List[Tuple[int, int]] = []

    # 候选对按 NSW 分数降序（平局取 (i, j) 字典序）——全局互惠最优意图。
    candidates: List[Tuple[float, int, int]] = []
    if same_set:
        # 同一集合（如 cohort）：general matching（无向图，i < j 无序对）。
        for i in range(M):
            for j in range(i + 1, N):
                nsw = _nsw_score(pref_matrix, i, j)
                if nsw <= 0:
                    continue
                candidates.append((nsw, i, j))
        candidates.sort(key=lambda t: (-t[0], t[1], t[2]))
        deg = [0] * M
        for _nsw, i, j in candidates:
            if deg[i] < b_max and deg[j] < b_max:
                matched_pairs.append((i, j))
                match_prob[i, j] = 1
                match_prob[j, i] = 1  # 无向匹配：对称存储
                deg[i] += 1
                deg[j] += 1
    else:
        # 二部图（如 market/batch）：b_max 绑定 member（左）侧，
        # pool_b_max 可选绑定 pool（右）侧（spec/05-boundaries.md §7）。
        for i in range(M):
            for j in range(N):
                nsw = _nsw_score(pref_matrix, i, j)
                if nsw <= 0:
                    continue
                candidates.append((nsw, i, j))
        candidates.sort(key=lambda t: (-t[0], t[1], t[2]))
        left_deg = [0] * M
        right_deg = [0] * N
        for _nsw, i, j in candidates:
            left_ok = left_deg[i] < b_max
            right_ok = pool_b_max is None or right_deg[j] < pool_b_max
            if left_ok and right_ok:
                matched_pairs.append((i, j))
                match_prob[i, j] = 1
                left_deg[i] += 1
                right_deg[j] += 1

    edges = _build_edges(pref_matrix, matched_pairs, w_embed, w_llm)
    envy_report = check_envy(pref_matrix, match_prob)
    _attach_b_min_report(envy_report, pref_matrix, match_prob, b_min)
    return edges, match_prob, envy_report


def check_envy(pref_matrix: PrefMatrix, match_prob: np.ndarray) -> dict:
    """检查匹配结果中的 envy 公平性（own-best 语义，基于完整匹配集）。

    语义（与 :func:`mutual.evaluate.evaluate` 的 envy 计数一致）：
    左节点 ``i`` 嫉妒 ``i2`` ⟺ ``i2`` 的匹配集中存在 ``j2``，使
    ``pref_left_to_right[i, j2]`` 严格大于 ``i`` 自己**最优**匹配的偏好值。
    右侧同构（用 ``pref_right_to_left``）。

    Args:
        pref_matrix: 双向偏好矩阵。
        match_prob: 匹配概率矩阵（0/1，确定性匹配；同集匹配须对称存储）。

    Returns:
        ``{"left_envy_count", "right_envy_count", "total_envy", "left", "right"}``，
        其中 ``left``/``right`` 为 ``(envier, envied)`` 索引对的列表。
    """
    M = len(pref_matrix.left_ids)
    N = len(pref_matrix.right_ids)

    left_matches: List[Set[int]] = [
        {j for j in range(N) if match_prob[i, j] > 0.5} for i in range(M)
    ]
    right_matches: List[Set[int]] = [
        {i for i in range(M) if match_prob[i, j] > 0.5} for j in range(N)
    ]

    left_envy: List[Tuple[int, int]] = []
    for i, own in enumerate(left_matches):
        if not own:
            continue
        own_best = max(float(pref_matrix.pref_left_to_right[i, j]) for j in own)
        for i2, other in enumerate(left_matches):
            if i2 == i or not other:
                continue
            if any(float(pref_matrix.pref_left_to_right[i, j2]) > own_best for j2 in other):
                left_envy.append((i, i2))

    right_envy: List[Tuple[int, int]] = []
    for j, own in enumerate(right_matches):
        if not own:
            continue
        own_best = max(float(pref_matrix.pref_right_to_left[j, i]) for i in own)
        for j2, other in enumerate(right_matches):
            if j2 == j or not other:
                continue
            if any(float(pref_matrix.pref_right_to_left[j, i2]) > own_best for i2 in other):
                right_envy.append((j, j2))

    return {
        "left_envy_count": len(left_envy),
        "right_envy_count": len(right_envy),
        "total_envy": len(left_envy) + len(right_envy),
        "left": left_envy,
        "right": right_envy,
    }


# ---------------------------------------------------------------------------
# 内部 helper（纯函数）
# ---------------------------------------------------------------------------


def _nsw_score(pref_matrix: PrefMatrix, i: int, j: int) -> float:
    """NSW 分数：双向偏好的几何平均（``sqrt(pref_lr * pref_rl)``）。"""
    a = float(pref_matrix.pref_left_to_right[i, j])
    b = float(pref_matrix.pref_right_to_left[j, i])
    return sqrt(a * b)


def _build_edges(
    pref_matrix: PrefMatrix,
    matched_pairs: List[Tuple[int, int]],
    w_embed: float,
    w_llm: float,
) -> List[Edge]:
    """把匹配对构造成 :class:`~mutual.schemas.Edge`。

    边方向：左节点 ``left_ids[i]`` × 右节点 ``right_ids[j]``（batch 模式
    即 member × pool）。
    ``final_weight``：blending 权重混合；因 match 阶段仅见偏好矩阵，
    embed/llm 均以 NSW 分数重建（混合退化为 ``(w_embed + w_llm) * nsw``）。
    """
    edges: List[Edge] = []
    for i, j in matched_pairs:
        user1 = pref_matrix.left_ids[i]
        user2 = pref_matrix.right_ids[j]
        a_to_b = float(pref_matrix.pref_left_to_right[i, j])
        b_to_a = float(pref_matrix.pref_right_to_left[j, i])
        nsw = sqrt(a_to_b * b_to_a)
        final_weight = (w_embed + w_llm) * nsw
        edges.append(
            Edge(
                user1=user1,
                user2=user2,
                pair_id=stable_pair_id(user1, user2),
                final_weight=final_weight,
                embed_score=nsw,
                llm_score=nsw,
                embed_score_normalized=None,
                llm_score_normalized=None,
                llm_score_a_to_b=a_to_b,
                llm_score_b_to_a=b_to_a,
                intro="",
                starter_topics="",
            )
        )
    edges.sort(key=lambda e: (-e.final_weight, e.pair_id))
    return edges


def _empty_envy() -> dict:
    return {
        "left_envy_count": 0,
        "right_envy_count": 0,
        "total_envy": 0,
        "left": [],
        "right": [],
    }


def _attach_b_min_report(
    report: dict,
    pref_matrix: PrefMatrix,
    match_prob: np.ndarray,
    b_min: int,
) -> None:
    """把 ``b_min`` 可行性字段附加进报告（qodo #9，原地修改）。

    度数口径：member 侧 = 左节点（二部图）或全部节点（同集无向图，
    此时所有节点都是 member，度数按 ``match_prob`` 行和计——对称存储下
    行和即总度数）。

    ``b_min <= 0`` 时不启用（报告仍写入 ``b_min`` 值与空 violations，
    保持报告 shape 稳定，便于下游消费）。
    """
    M = len(pref_matrix.left_ids)
    N = len(pref_matrix.right_ids)
    same_set = list(pref_matrix.left_ids) == list(pref_matrix.right_ids)

    if same_set:
        degrees = {pref_matrix.left_ids[i]: int(match_prob[i].sum()) for i in range(M)}
    else:
        degrees = {
            pref_matrix.left_ids[i]: int(match_prob[i, :].sum()) for i in range(M)
        }
    # N == 0 时 match_prob 形状 [M, 0]，行和为 0——全部 member 违约，正确。
    violations = [uid for uid, deg in degrees.items() if deg < b_min]
    report["b_min"] = b_min
    report["b_min_violations"] = violations
    report["b_min_satisfied"] = not violations


def _to_int(value: Any, default: int) -> int:
    try:
        return int(value)
    except (TypeError, ValueError):
        return default


def _to_float(value: Any, default: float) -> float:
    try:
        return float(value)
    except (TypeError, ValueError):
        return default
