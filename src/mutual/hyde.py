"""Mutual — HyDE 生成。

对应 docs/engineering-plan.md §3.4、spec/02-stages.md §2。

Hypothetical Document Embeddings：为每个 section 生成假设性描述，
增强 embedding 语义匹配（如 ``"需要技术合作"`` → ``"寻找会 Python 的开发者"``）。
``generate_hyde`` 是纯变换；``load_hyde`` / ``dump_hyde`` 是 adapter 用的磁盘 helper。

config 读取：``hyde.n_descriptors``（默认 1）、``prompts.hyde_prompt_text``、
``models.pair_llm`` 等。支持 ``n_descriptors > 1`` end-to-end
（相似度阶段对 descriptor pairs 做 max-pool）。

边界：
- 填充为 ``"Not specified"`` 的 section 不生成描述符（缺失 = 中性，
  spec/05-boundaries.md §1；其 HyDE 向量在 embed 层为零向量）。
- spec 沉默 A-11：hyde 无 ``failed_out`` 契约。单个 section 的 LLM 调用
  失败时该 section 得到空描述符列表，pipeline 继续，不中断。
"""

from __future__ import annotations

import json
from typing import Any, Dict, List

from .config import resolve_prompt_templates
from .extract import NOT_SPECIFIED
from .schemas import ExtractedSections, HydeDescriptors


def _parse_descriptors(response: Any, n_descriptors: int) -> List[str]:
    """解析 LLM 响应为最多 ``n_descriptors`` 条描述符。

    spec 沉默 A-15：响应格式未在 spec 中固定。解析顺序：
    1. JSON 数组（``["d1", "d2"]``）→ 取前 n 条非空字符串；
    2. 自由文本 → 按行切分，剥离 ``-`` / ``*`` / ``1.`` 等项目符号，
       取前 n 条非空行。
    """
    if not isinstance(response, str):
        return []
    text = response.strip()
    try:
        data = json.loads(text)
    except (ValueError, TypeError):
        data = None
    if isinstance(data, list):
        items = [str(x).strip() for x in data if isinstance(x, str) and x.strip()]
        return items[:n_descriptors]

    lines: List[str] = []
    for raw in text.splitlines():
        line = raw.strip()
        while line.startswith(("-", "*", "•")):
            line = line[1:].strip()
        if len(line) >= 2 and line[0].isdigit() and line[1] in (".", ")"):
            line = line[2:].strip()
        if line:
            lines.append(line)
    return lines[:n_descriptors]


def generate_hyde(
    sections: List[ExtractedSections],
    config: Dict[str, Any],
    llm_wrapper: Any,
) -> Dict[str, HydeDescriptors]:
    """为每个 section 生成 ``n_descriptors`` 个假设性描述。

    所有可调参数从 ``config`` 读取，不硬编码。

    Args:
        sections: ``list[ExtractedSections]``。
        config: 配置 dict（读取 ``hyde.n_descriptors``、``prompts.hyde_prompt_text``、
            ``models.pair_llm`` 等）。
        llm_wrapper: :class:`~mutual.llm.LLMWrapper` 实例（鸭子类型）。

    Returns:
        ``dict[user_id → HydeDescriptors]``，按 user_id 索引。
        每个 ``HydeDescriptors.descriptors`` 形如
        ``{section: [desc1, desc2, ...]}``。
    """
    n_descriptors = int(config.get("hyde", {}).get("n_descriptors", 1))
    template = resolve_prompt_templates(config)["hyde"]
    model = config.get("models", {}).get("pair_llm")

    result: Dict[str, HydeDescriptors] = {}
    for es in sections:
        descriptors: Dict[str, List[str]] = {}
        for name, content in es.sections.items():
            if not content or content == NOT_SPECIFIED:
                continue
            try:
                prompt = template.format(
                    section_name=name,
                    section_content=content,
                    n_descriptors=n_descriptors,
                )
                response = llm_wrapper([{"role": "user", "content": prompt}], model=model)
            except Exception:
                continue
            descriptors[name] = _parse_descriptors(response, n_descriptors)
        result[es.id] = HydeDescriptors(id=es.id, descriptors=descriptors)
    return result


def load_hyde(path: str) -> Dict[str, HydeDescriptors]:
    """从磁盘加载 HyDE（adapter 用）。

    Args:
        path: JSON 文件路径（``list[{id, descriptors}, ...]`` 或同结构 dict）。

    Returns:
        ``dict[user_id → HydeDescriptors]``。
    """
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    if isinstance(data, dict):
        # 同结构 dict：{user_id: {section: [desc, ...]}}
        return {uid: HydeDescriptors(id=uid, descriptors=d) for uid, d in data.items()}
    return {hd.id: hd for hd in (HydeDescriptors.from_dict(d) for d in data)}


def dump_hyde(hyde: Dict[str, HydeDescriptors], path: str) -> None:
    """写入 HyDE 到磁盘（adapter 用）。

    Args:
        hyde: ``dict[user_id → HydeDescriptors]``。
        path: 目标 JSON 文件路径。
    """
    with open(path, "w", encoding="utf-8") as f:
        json.dump(
            [hyde[uid].to_dict() for uid in sorted(hyde)],
            f,
            ensure_ascii=False,
            indent=2,
        )
