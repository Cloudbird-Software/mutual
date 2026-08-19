"""Mutual — 对接话术。

对应 docs/engineering-plan.md §3.7、spec/02-stages.md §9。

为每对匹配生成双向对接话术（``For A: ...`` / ``For B: ...``）+ 破冰话题
（``starter_topics``）。``generate_introductions_for_matches`` 是纯变换。

边界：LLM 失败时 :func:`attach_fallback_intro` 生成模板话术，
保证每条 Edge 都有非空 ``intro`` / ``starter_topics``。
"""

from __future__ import annotations

import json
from dataclasses import replace
from typing import Any, Dict, List, Optional, Tuple

from .schemas import Edge, Introduction


def generate_introductions_for_matches(
    edges: List[Edge],
    sections_dict: Dict[str, Dict[str, str]],
    instruction: str,
    prompt_template: str,
    llm_wrapper: Any,
    **kwargs: Any,
) -> Dict[str, Introduction]:
    """为每对匹配生成双向对接话术 + 破冰话题。

    所有可调参数从 caller 注入（prompt 模板、模型等），不硬编码。

    Args:
        edges: 匹配边列表（来自 match 阶段）。
        sections_dict: ``dict[user_id → sections]``（由
            :func:`mutual.score.create_sections_dict` 构造）。
        instruction: 话术指令。
        prompt_template: 话术 prompt 模板。
        llm_wrapper: :class:`~mutual.llm.LLMWrapper` 实例（鸭子类型）。
        **kwargs: 预留扩展（如 ``display_names``、``config``）。

    Returns:
        ``dict[pair_id → Introduction]``。LLM 失败的 pair 由
        :func:`attach_fallback_intro` 兜底，保证不缺项。
    """
    display_names: Dict[str, str] = dict(kwargs.get("display_names") or {})
    model = kwargs.get("model")
    out: Dict[str, Introduction] = {}
    for edge in edges:
        prompt = _build_intro_prompt(edge, sections_dict, instruction, prompt_template)
        messages: List[Dict[str, str]] = [{"role": "user", "content": prompt}]
        call_kwargs: Dict[str, Any] = {}
        if model:
            call_kwargs["model"] = model
        try:
            raw = llm_wrapper(messages, **call_kwargs)
        except Exception:
            raw = None
        parsed = _parse_intro_response(raw) if raw else None
        if parsed is None:
            fallback_edge = attach_fallback_intro(edge, display_names)
            out[edge.pair_id] = Introduction(
                pair_id=edge.pair_id,
                intro=fallback_edge.intro,
                starter_topics=fallback_edge.starter_topics,
            )
        else:
            intro, starter_topics = parsed
            out[edge.pair_id] = Introduction(
                pair_id=edge.pair_id, intro=intro, starter_topics=starter_topics
            )
    return out


def attach_fallback_intro(
    edge: Edge,
    display_names: Optional[Dict[str, str]] = None,
) -> Edge:
    """LLM 失败时生成模板话术，返回带 ``intro`` / ``starter_topics`` 的 Edge 副本。

    不修改原 Edge（纯函数）；返回一个填充了模板话术的新 Edge。
    模板基于 edge 的 user1/user2 与可选的展示名映射。

    Args:
        edge: 待补充话术的匹配边。
        display_names: 可选 ``{user_id → 展示名}``；缺省时用 user_id。

    Returns:
        带非空 ``intro`` / ``starter_topics`` 的 Edge 副本。
    """
    names = display_names or {}
    name1 = names.get(edge.user1, edge.user1)
    name2 = names.get(edge.user2, edge.user2)
    intro = (
        f"For {name1}: {name2} looks like a promising connection based on your "
        f"profiles — their background may complement what you're looking for.\n"
        f"For {name2}: {name1} looks like a promising connection based on your "
        f"profiles — their background may complement what you're looking for."
    )
    starter_topics = (
        "What each of you is currently working on; where your goals overlap; "
        "one concrete way you could help each other this month."
    )
    return replace(edge, intro=intro, starter_topics=starter_topics)


# ---------------------------------------------------------------------------
# 内部 helper（纯函数）
# ---------------------------------------------------------------------------


class _MissingKeyDict(dict):
    def __missing__(self, key: str) -> str:
        return ""


def _safe_format(template: str, mapping: Dict[str, str]) -> str:
    try:
        return template.format_map(_MissingKeyDict(mapping))
    except (ValueError, IndexError):
        return template


def _format_sections(sections: Optional[Dict[str, str]]) -> str:
    if not sections:
        return "Not specified"
    return "\n".join(f"{k}: {v}" for k, v in sorted(sections.items()))


def _build_intro_prompt(
    edge: Edge,
    sections_dict: Dict[str, Dict[str, str]],
    instruction: str,
    prompt_template: str,
) -> str:
    """构造双向话术 prompt（不得包含打分标记 ``a_to_b``，避免 fake 路由错判）。"""
    return _safe_format(
        prompt_template,
        {
            "user1_name": edge.user1,
            "user2_name": edge.user2,
            "user1_sections": _format_sections(sections_dict.get(edge.user1)),
            "user2_sections": _format_sections(sections_dict.get(edge.user2)),
            "instruction": instruction,
            "user1": edge.user1,
            "user2": edge.user2,
        },
    )


def _parse_intro_response(text: str) -> Optional[Tuple[str, str]]:
    """解析 ``{"intro": str, "starter_topics": str}``；失败返回 None（走 fallback）。"""
    s = text.strip()
    if s.startswith("```"):
        s = s.split("\n", 1)[1] if "\n" in s else s
        s = s.rstrip()
        if s.endswith("```"):
            s = s[:-3].rstrip()
    try:
        obj = json.loads(s)
    except json.JSONDecodeError:
        start = s.find("{")
        end = s.rfind("}")
        if start == -1 or end <= start:
            return None
        try:
            obj = json.loads(s[start : end + 1])
        except json.JSONDecodeError:
            return None
    if not isinstance(obj, dict):
        return None
    intro = obj.get("intro")
    topics = obj.get("starter_topics")
    if not isinstance(intro, str) or not isinstance(topics, str):
        return None
    if not intro.strip() or not topics.strip():
        return None
    return intro, topics
