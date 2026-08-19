"""Mutual — 匹配报告。

对应 docs/engineering-plan.md §3.10、spec/02-stages.md §10。

``create_report`` 是纯变换：每用户 top-N 匹配列表（按 ``final_weight`` 排序）
+ 群组摘要（总边数 / 平均度 / 分数统计）。``scope_user_ids`` 限定报告范围
（batch 模式只报 member 侧）。
"""

from __future__ import annotations

from typing import Dict, List, Optional

from .schemas import Edge, ExtractedSections


def create_report(
    edges: List[Edge],
    extracted: List[ExtractedSections],
    top_matches_per_user: int,
    scope_user_ids: Optional[List[str]] = None,
) -> Dict:
    """生成人类可读的匹配报告（用户报告 + 群组摘要）。

    Args:
        edges: 最终匹配边列表（来自 match 阶段或 runners 兜底）。
        extracted: 全量 ``list[ExtractedSections]``（``scope_user_ids=None`` 时
            以其 id 集合作为报告的用户全集）。
        top_matches_per_user: 每用户最多展示几条匹配（按 ``final_weight`` 降序）；
            ``None`` 或非正值表示不限（防御性宽容）。
        scope_user_ids: 限定报告范围；``None`` 表示全部用户。

    Returns:
        ``dict``，结构（对齐 tests/golden/test_basic/cohort.json 的形状）::

            {
              "overview": {total_users, total_edges, average_degree,
                           edges_with_llm_scores, edges_with_directional_scores},
              "degree_distribution": {"<degree>": <count>, ...},
              "score_statistics": {final_weights/embedding_scores/llm_scores:
                                   {min, max, avg}},
              "users": {uid: {degree, matches: [{partner, weight,
                                                 directional_scores}]}}
            }

    边界：
    - 纯函数，不修改输入 edges/extracted。
    - scope 限定的是"给谁出报告"；与 scoped 用户相邻的边计入统计，
      对端（如 batch 模式的 pool 侧）只作为 partner 出现。
    """
    if scope_user_ids is not None:
        scope = list(scope_user_ids)
    else:
        scope = [es.id for es in extracted]
    scope_set = set(scope)

    relevant = [e for e in edges if e.user1 in scope_set or e.user2 in scope_set]

    by_user: Dict[str, List[Edge]] = {uid: [] for uid in scope}
    for edge in relevant:
        if edge.user1 in scope_set:
            by_user[edge.user1].append(edge)
        if edge.user2 in scope_set:
            by_user[edge.user2].append(edge)

    users: Dict[str, Dict] = {}
    for uid in scope:
        entries = sorted(by_user[uid], key=lambda e: (-e.final_weight, e.pair_id))
        capped = entries
        if top_matches_per_user is not None and top_matches_per_user > 0:
            capped = entries[:top_matches_per_user]
        users[uid] = {
            "degree": len(entries),
            "matches": [_match_entry(edge, uid) for edge in capped],
        }

    degrees = [info["degree"] for info in users.values()]
    degree_distribution: Dict[str, int] = {}
    for d in sorted(degrees):
        key = str(d)
        degree_distribution[key] = degree_distribution.get(key, 0) + 1

    total_edges = len(relevant)
    avg_degree = round(sum(degrees) / len(scope), 3) if scope else 0.0

    llm_values = [
        v for e in relevant for v in (e.llm_score_a_to_b, e.llm_score_b_to_a) if v is not None
    ]
    if not llm_values:
        llm_values = [e.llm_score for e in relevant if e.llm_score is not None]

    overview = {
        "total_users": len(scope),
        "total_edges": total_edges,
        "average_degree": avg_degree,
        "edges_with_llm_scores": sum(
            1 for e in relevant if e.llm_score_a_to_b is not None or e.llm_score_b_to_a is not None
        ),
        "edges_with_directional_scores": sum(
            1 for e in relevant if e.llm_score_a_to_b is not None and e.llm_score_b_to_a is not None
        ),
    }

    return {
        "overview": overview,
        "degree_distribution": degree_distribution,
        "score_statistics": {
            "final_weights": _stats([e.final_weight for e in relevant]),
            "embedding_scores": _stats([e.embed_score for e in relevant]),
            "llm_scores": _stats(llm_values),
        },
        "users": users,
    }


def _match_entry(edge: Edge, uid: str) -> Dict:
    partner = edge.user2 if edge.user1 == uid else edge.user1
    entry: Dict = {"partner": partner, "weight": round(edge.final_weight, 3)}
    if edge.llm_score_a_to_b is not None or edge.llm_score_b_to_a is not None:
        entry["directional_scores"] = {
            "a_to_b": round(edge.llm_score_a_to_b, 3)
            if edge.llm_score_a_to_b is not None
            else None,
            "b_to_a": round(edge.llm_score_b_to_a, 3)
            if edge.llm_score_b_to_a is not None
            else None,
        }
    else:
        entry["directional_scores"] = None
    return entry


def _stats(values: List[float]) -> Dict:
    """min/max/avg 统计（round 3，与 golden fixture 精度一致）；空列表 → None。"""
    if not values:
        return {"min": None, "max": None, "avg": None}
    return {
        "min": round(min(values), 3),
        "max": round(max(values), 3),
        "avg": round(sum(values) / len(values), 3),
    }
