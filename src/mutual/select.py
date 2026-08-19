"""Mutual — 候选对选择。

对应 docs/engineering-plan.md §3.7、spec/02-stages.md §5。

从相似度矩阵中贪心轮转选择进入 LLM 精排的候选对。
``select_pairs`` 是纯函数，无副作用。

边界（spec/05-boundaries.md §8、spec/02-stages.md §5）：
- per-profile cap：每个用户（member 与 pool 侧同权）最多入选
  ``budgets.max_n_llm_evaluations_per_profile`` 对。
- global cap：总入选对数 ≤ ``budgets.max_pair_llm_calls``。
- novelty：``excluded_pairs``（adapter 按 novelty 窗口构建）中的 pair
  不参与选择。
- 只保留正相似度对（``fused_matrix`` 分数 ≤ 0 的不选）。

spec 沉默：
- A-7：轮转顺序与平局裁决未在 spec 中定义。实现：每轮按
  ``source_ids`` 顺序轮流为每个用户取其当前最优候选；分数平局取
  字典序较小的 partner；返回列表按选择顺序排列。
- A-8：选择依据用 ``fused_matrix``（dir 的最终融合视图）。
- A-9：``max_pair_llm_calls`` 语义 = 入选 pair 总数上限（每 pair 至少
  一次 LLM 调用）。
- A-10：M×N 模式下 source 与 target 重叠用户的自配对（u,u）排除；
  pool 侧用户同样计入 per-profile cap。
"""

from __future__ import annotations

from typing import Any, Dict, List, Optional, Set

from .schemas import CandidatePair, SimilarityResult, stable_pair_id


def select_pairs(
    similarity: SimilarityResult,
    budgets: Dict[str, Any],
    excluded_pairs: Optional[Set[str]] = None,
) -> List[CandidatePair]:
    """贪心轮转选择候选对（per-profile cap + global cap + novelty 排除）。

    Args:
        similarity: 相似度结果（选择依据 = ``fused_matrix``，A-8）。
        budgets: 预算 dict，读取 ``max_n_llm_evaluations_per_profile``
            （per-profile cap）与 ``max_pair_llm_calls``（global cap，A-9）；
            缺省/None = 不设上限。
        excluded_pairs: 排除的 pair_id 集合（novelty 窗口内已暴露的对，
            spec/05-boundaries.md §8）。

    Returns:
        ``list[CandidatePair]``，按贪心选择顺序排列（A-7）；
        ``user1 < user2``，``pair_id = stable_pair_id(user1, user2)``。
    """
    excluded: Set[str] = set(excluded_pairs) if excluded_pairs else set()
    per_cap: Optional[int] = budgets.get("max_n_llm_evaluations_per_profile")
    global_cap: Optional[int] = budgets.get("max_pair_llm_calls")

    src_ids = list(similarity.source_ids)
    tgt_ids = list(similarity.target_ids)
    fused = similarity.fused_matrix
    square = similarity.is_square

    # 每个源侧用户的候选列表（正相似度、非排除、非自配对）。
    # square 模式 fused 对称（legacy 路径），每个用户考虑全部 partner
    # （上三角值镜像），pair 去重由 chosen 集合保证。
    candidates: Dict[str, List[Any]] = {uid: [] for uid in src_ids}
    for i, uid in enumerate(src_ids):
        for j, other in enumerate(tgt_ids):
            if other == uid:
                continue
            if square and i > j:
                score = float(fused[j, i])  # 上三角镜像（对称）
            else:
                score = float(fused[i, j])
            if score <= 0.0:
                continue
            if stable_pair_id(uid, other) in excluded:
                continue
            candidates[uid].append((other, score))
    for uid in candidates:
        # 分数降序；平局取字典序较小的 partner（A-7）。
        candidates[uid].sort(key=lambda t: (-t[1], t[0]))

    counts: Dict[str, int] = {uid: 0 for uid in set(src_ids) | set(tgt_ids)}
    chosen: Set[str] = set()
    result: List[CandidatePair] = []
    total = 0

    def _at_cap(uid: str) -> bool:
        return per_cap is not None and counts[uid] >= per_cap

    while global_cap is None or total < global_cap:
        progressed = False
        for uid in src_ids:
            if global_cap is not None and total >= global_cap:
                break
            if _at_cap(uid):
                continue
            pick = None
            for other, score in candidates[uid]:
                if _at_cap(other):
                    continue
                pid = stable_pair_id(uid, other)
                if pid in chosen:
                    continue
                pick = (other, score, pid)
                break
            if pick is None:
                continue
            other, score, pid = pick
            result.append(CandidatePair.create(uid, other, score))
            chosen.add(pid)
            counts[uid] += 1
            counts[other] += 1
            total += 1
            progressed = True
        if not progressed:
            break
    return result
