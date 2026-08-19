"""Mutual — 配置加载器。

从 YAML 加载默认配置，支持目录 overlay 和单值 override。
"""

from __future__ import annotations

from pathlib import Path
from typing import Any, Dict, Optional

import yaml

_DEFAULT_CONFIG_PATH = Path(__file__).parent.parent.parent / "config" / "default.yaml"


def load_config(
    config_dir: Optional[str] = None,
    overrides: Optional[Dict[str, Any]] = None,
) -> Dict[str, Any]:
    """加载配置：默认 → 目录 overlay → 单值 override。

    Args:
        config_dir: overlay 目录路径。该目录下的 YAML 文件按文件名
                    匹配默认配置的顶层 key（如 blending.yaml 覆盖 blending）。
        overrides: 点号路径的 override，如 {"blending.embed_weight": 0.4}。
    """
    with open(_DEFAULT_CONFIG_PATH, "r", encoding="utf-8") as f:
        config = yaml.safe_load(f)

    if config_dir:
        config = _apply_dir_overlay(config, Path(config_dir))

    if overrides:
        for dotted_key, value in overrides.items():
            _set_dotted(config, dotted_key, value)

    return config


def _apply_dir_overlay(config: Dict, dir_path: Path) -> Dict:
    """目录 overlay：每个 YAML 文件覆盖对应顶层 key。"""
    if not dir_path or not dir_path.is_dir():
        return config
    for yaml_file in sorted(dir_path.glob("*.yaml")):
        key = yaml_file.stem  # blending.yaml → "blending"
        with open(yaml_file, "r", encoding="utf-8") as f:
            overlay = yaml.safe_load(f)
        if overlay and key in config:
            config[key] = _deep_merge(config[key], overlay)
    return config


def _deep_merge(base: Dict, overlay: Dict) -> Dict:
    """递归合并：overlay 的值覆盖 base。"""
    result = dict(base)
    for k, v in overlay.items():
        if k in result and isinstance(result[k], dict) and isinstance(v, dict):
            result[k] = _deep_merge(result[k], v)
        else:
            result[k] = v
    return result


def _set_dotted(config: Dict, dotted_key: str, value: Any) -> None:
    """点号路径设值：blending.embed_weight = 0.4"""
    keys = dotted_key.split(".")
    d = config
    for k in keys[:-1]:
        d = d.setdefault(k, {})
    d[keys[-1]] = value


def resolve_prompt_templates(
    config: Dict[str, Any],
    prompt_paths: Optional[Dict[str, str]] = None,
) -> Dict[str, str]:
    """解析 prompt 模板：config 内联 > 外部文件 > 内置默认。"""
    prompts_config = config.get("prompts", {})
    templates: Dict[str, str] = {}

    defaults = {
        "scoring": _DEFAULT_SCORING_PROMPT,
        "introduction": _DEFAULT_INTRO_PROMPT,
        "section": _DEFAULT_SECTION_PROMPT,
        "hyde": _DEFAULT_HYDE_PROMPT,
    }

    for name, default in defaults.items():
        inline_key = f"{name}_prompt_text"
        if prompts_config.get(inline_key):
            templates[name] = prompts_config[inline_key]
        elif prompt_paths and name in prompt_paths:
            with open(prompt_paths[name], "r", encoding="utf-8") as f:
                templates[name] = f.read()
        else:
            templates[name] = default

    return templates


# ---------------------------------------------------------------------------
# 内置默认 prompt（Phase 1 可被外部文件替换）
# ---------------------------------------------------------------------------

_DEFAULT_SCORING_PROMPT = """You are a matchmaking expert. Score the potential connection between two people.

Person A (user1):
{user1_sections}

Person B (user2):
{user2_sections}

Instruction: {instruction}

Score from two directions:
1. How valuable is this connection for Person A? (A→B score, 0.0-1.0)
2. How valuable is this connection for Person B? (B→A score, 0.0-1.0)

Respond in JSON:
{{"a_to_b": <float>, "b_to_a": <float>, "reasoning": "<brief>"}}
"""

_DEFAULT_INTRO_PROMPT = """Write a personalized introduction for a matched pair.

Person A: {user1_name}
{user1_sections}

Person B: {user2_name}
{user2_sections}

Write two paragraphs:
- "For {user1_name}: ..." explaining why they should connect with Person B.
- "For {user2_name}: ..." explaining why they should connect with Person A.

Also suggest 2-3 starter topics for their first conversation.
"""

_DEFAULT_SECTION_PROMPT = """Extract structured sections from this profile text.

Profile text:
{raw_text}

Extract into these sections (use "Not specified" if not found):
- skills: What can this person do? What are their technical/creative capabilities?
- vision: What are they passionate about? What drives them?
- project: What are they currently working on or want to build?
- needs: What are they looking for? What help do they need?

Respond in JSON:
{{"skills": "...", "vision": "...", "project": "...", "needs": "..."}}
"""

_DEFAULT_HYDE_PROMPT = """Given this section content, write a hypothetical description
that would semantically match people who should connect with this person.

Section: {section_name}
Content: {section_content}

Write {n_descriptors} hypothetical description(s), each 1-2 sentences.
"""
