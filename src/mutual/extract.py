"""Mutual — Profile 提取。

对应 docs/engineering-plan.md §3.3、spec/02-stages.md §1。

LLM 从自由文本画像提取结构化分节（skills / vision / project / needs）。
``extract_sections`` 是纯变换：不碰文件系统、数据库、网络
（``load_sections`` / ``dump_sections`` 是 adapter 用的磁盘 helper）。

边界（spec/05-boundaries.md §4）：
- 提取失败的 section 填 ``"Not specified"``，pipeline 继续运行。
- ``failed_out`` 报告失败项；adapter **不得持久化**失败结果
  （否则永远不会重试）。
- sections 内容 hash 用 ``hash_text(json.dumps(sections, sort_keys=True))``，
  驱动后续 embedding 复用；禁止内置 ``hash()``。
"""

from __future__ import annotations

import json
from typing import Any, Dict, List, Optional

from .config import resolve_prompt_templates
from .schemas import ExtractedSections, Profile

#: 提取失败 / 未提及的 section 填充值（spec/01-schemas.md §2、spec/02-stages.md §1）。
NOT_SPECIFIED = "Not specified"

#: canonical section 词表（spec/01-schemas.md §1/§2 的固定四节）。
#: spec 沉默 A-1：该词表未参数化进 config，按 spec §1/§2 的固定四节处理。
_CANONICAL_SECTIONS = ("skills", "vision", "project", "needs")


def _strip_code_fences(text: str) -> str:
    """容错提取 JSON 主体：取首个 ``{`` 到末个 ``}`` 之间的子串。"""
    start = text.find("{")
    end = text.rfind("}")
    if start != -1 and end > start:
        return text[start : end + 1]
    return text


def _parse_response(response: Any) -> Optional[Dict[str, str]]:
    """解析 LLM 响应为 ``{section: text}``；不可解析返回 ``None``。"""
    if not isinstance(response, str):
        return None
    try:
        data = json.loads(_strip_code_fences(response))
    except (ValueError, TypeError):
        return None
    if not isinstance(data, dict):
        return None
    return {k: v for k, v in data.items() if isinstance(k, str) and isinstance(v, str)}


def _is_present(value: Any) -> bool:
    """section 值是否为有效内容（非空、非 "Not specified" 占位）。"""
    if not isinstance(value, str):
        return False
    stripped = value.strip()
    return bool(stripped) and stripped.lower() != NOT_SPECIFIED.lower()


def extract_sections(
    profiles: List[Profile],
    config: Dict[str, Any],
    llm_wrapper: Any,
    failed_out: Optional[List[str]] = None,
) -> List[ExtractedSections]:
    """LLM 从 Profile 自由文本提取 skills/vision/project/needs。

    所有可调参数从 ``config`` 读取（prompt 模板、模型、temperature 等），不硬编码。

    Args:
        profiles: 原始画像列表（``Profile``）。
        config: 配置 dict（读取 ``prompts.section_prompt_text``、
            ``models.pair_llm``、``models.reasoning_effort`` 等）。
        llm_wrapper: :class:`~mutual.llm.LLMWrapper` 实例（鸭子类型）。
        failed_out: 可选 out-param；调用方传入一个 list，本函数向其 append
            提取失败的 profile id。adapter 据此跳过持久化（§4）。

    Returns:
        ``list[ExtractedSections]``，失败 section 填 ``"Not specified"``，
        保证与输入等长、按 id 对齐。

    边界：失败结果不持久化（spec/05-boundaries.md §4）。
    """
    template = resolve_prompt_templates(config)["section"]
    model = config.get("models", {}).get("pair_llm")

    results: List[ExtractedSections] = []
    for profile in profiles:
        parsed: Optional[Dict[str, str]] = None
        try:
            raw_text = "\n".join(f"{name}: {text}" for name, text in profile.sections.items())
            prompt = template.format(raw_text=raw_text)
            response = llm_wrapper([{"role": "user", "content": prompt}], model=model)
            parsed = _parse_response(response)
        except Exception:
            parsed = None

        sections: Dict[str, str] = {}
        failed = False
        for name in _CANONICAL_SECTIONS:
            value = (parsed or {}).get(name)
            if _is_present(value):
                sections[name] = str(value).strip()
            else:
                sections[name] = NOT_SPECIFIED
                failed = True

        if failed and failed_out is not None:
            failed_out.append(profile.id)
        results.append(ExtractedSections(id=profile.id, sections=sections))
    return results


def load_sections(path: str) -> List[ExtractedSections]:
    """从磁盘加载 ``ExtractedSections`` 列表（adapter 用）。

    Args:
        path: JSON 文件路径（``list[{id, sections, hash}, ...]``）。

    Returns:
        ``list[ExtractedSections]``。
    """
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    return [ExtractedSections.from_dict(d) for d in data]


def dump_sections(sections: List[ExtractedSections], path: str) -> None:
    """写入 ``ExtractedSections`` 列表到磁盘（adapter 用）。

    Args:
        sections: 待持久化的 sections 列表（不应含失败项，§4）。
        path: 目标 JSON 文件路径。
    """
    with open(path, "w", encoding="utf-8") as f:
        json.dump(
            [s.to_dict() for s in sections],
            f,
            ensure_ascii=False,
            indent=2,
        )
