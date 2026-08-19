"""Mutual — Pipeline 阶段注册表（StageSpec）。

每个 StageSpec 声明一个纯变换的 IO 契约：
  name + description + input_schema + output_schema + run + load + dump

外部 caller 无需读源码即可通过 stages.describe_stage(name) 了解每阶段需要什么。
实现代码可以随时重写，但 StageSpec 的 name/io_schema 不可随意修改。
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Callable, Dict, List, Optional

from .embed import dump_bundle, embed_sections, load_bundle
from .evaluate import evaluate
from .extract import dump_sections, extract_sections, load_sections
from .hyde import dump_hyde, generate_hyde, load_hyde
from .introduce import generate_introductions_for_matches
from .match import solve_match
from .report import create_report
from .score import build_pref_matrix, score_pairs_with_llm
from .select import select_pairs
from .similarity import compute_similarity

# ---------------------------------------------------------------------------
# StageSpec
# ---------------------------------------------------------------------------


@dataclass
class StageSpec:
    """一个 pipeline 阶段：纯变换 + 声明式 IO schema + disk helpers。

    约定：
    - run 是纯函数：输入是 plain arguments，输出是 dataclass。
    - load/dump 是可选的磁盘序列化 helper（adapter 用，core 不用）。
    - notes 记录非显然的语义约束（对应 spec/05-boundaries.md）。
    """

    name: str
    description: str
    input_schema: Dict[str, str]
    # output_schema 既可能是字段级 dict，也可能是单个类型名/类型表达式 str
    # （见 spec/02-stages.md：部分 stage 的输出是单一 dataclass 或映射描述）。
    output_schema: Dict[str, str] | str
    run: Callable[..., Any]
    load: Optional[Callable] = None
    dump: Optional[Callable] = None
    notes: str = ""
    extra: Dict[str, Any] = field(default_factory=dict)


# ---------------------------------------------------------------------------
# Registry
# ---------------------------------------------------------------------------

_REGISTRY: Dict[str, StageSpec] = {}


def register(spec: StageSpec) -> StageSpec:
    """注册一个 stage。重复注册抛 ValueError。"""
    if spec.name in _REGISTRY:
        raise ValueError(f"Stage '{spec.name}' already registered")
    _REGISTRY[spec.name] = spec
    return spec


def get_stage(name: str) -> StageSpec:
    if name not in _REGISTRY:
        raise KeyError(f"Stage '{name}' not found. Available: {list(_REGISTRY.keys())}")
    return _REGISTRY[name]


def describe_stage(name: str) -> Dict[str, Any]:
    """返回某阶段的 JSON-serializable 描述（外部 caller 用）。"""
    spec = get_stage(name)
    return {
        "name": spec.name,
        "description": spec.description,
        "input_schema": spec.input_schema,
        "output_schema": spec.output_schema,
        "notes": spec.notes,
    }


def list_stages() -> List[str]:
    return list(_REGISTRY.keys())


def describe_all() -> List[Dict[str, Any]]:
    return [describe_stage(n) for n in _REGISTRY]


# ---------------------------------------------------------------------------
# Stage 列表（顺序 = pipeline 执行顺序）
# ---------------------------------------------------------------------------

# Schema 速记
_SCORES_SCHEMA = "dict[pair_id → PairScore]"
_EDGES_SCHEMA = "list[Edge]"
_SECTIONS_SCHEMA = "list[ExtractedSections]"


# === 以下 register 调用引用各 stage 模块的 run/load/dump ===
# 各模块在 Phase 1 实现时导入；此处先注册占位以确立契约。
# Phase 0（当前）：用 stub 函数注册，确保契约可被外部 caller 读取。


register(
    StageSpec(
        name="extract",
        description="LLM 从自由文本画像提取结构化分节（skills/vision/project/needs）。",
        input_schema={
            "profiles": "list[Profile]",
            "config": "dict",
        },
        output_schema=_SECTIONS_SCHEMA,
        run=extract_sections,
        load=load_sections,
        dump=dump_sections,
        notes=(
            "边界：提取失败填 'Not specified'，pipeline 继续运行。"
            "failed_out 参数报告失败项；adapter 不得持久化失败结果。"
            "见 spec/05-boundaries.md §4。"
        ),
    )
)

register(
    StageSpec(
        name="hyde",
        description="为每个 section 生成假设性描述，增强 embedding 语义匹配。",
        input_schema={
            "sections": _SECTIONS_SCHEMA,
            "config": "dict (hyde.n_descriptors)",
        },
        output_schema="dict[user_id → HydeDescriptors]",
        run=generate_hyde,
        load=load_hyde,
        dump=dump_hyde,
        notes="n_descriptions 默认 1，可配。支持 >1 end-to-end（max-pool over descriptor pairs）。",
    )
)

register(
    StageSpec(
        name="embed",
        description="生成 section + HyDE 向量；content-hash 驱动增量复用。",
        input_schema={
            "sections": _SECTIONS_SCHEMA,
            "hyde": "dict[user_id → HydeDescriptors]",
            "config": "dict (models.embedding, models.embedding_dimensions)",
            "existing": "EmbeddingsBundle | None",
        },
        output_schema="EmbeddingsBundle",
        run=embed_sections,
        load=load_bundle,
        dump=dump_bundle,
        notes=(
            "复用是 content-addressed（section_hashes），不是 roster-addressed。"
            "不同 model 的 bundle 整体忽略。"
            "全尺寸存储；MRL 截断在工作副本上做。"
            "见 spec/05-boundaries.md §6。"
        ),
    )
)

register(
    StageSpec(
        name="similarity",
        description="计算方向性相似度矩阵（rectangular M×N 或 square N×N）。",
        input_schema={
            "source": "EmbeddingsBundle",
            "target": "EmbeddingsBundle | None (None = N×N square)",
            "recipe_config": "dict (section_weights, cross_section_weights)",
        },
        output_schema="SimilarityResult",
        run=compute_similarity,
        notes=(
            "缺失 section = 中性（mask + 分母修正），不是零。"
            "方向性 cross-term 不盲目对称化。"
            "见 spec/05-boundaries.md §1, §2。"
        ),
    )
)

register(
    StageSpec(
        name="select",
        description="贪心轮转选择候选对，per-profile cap + global cap + novelty 排除。",
        input_schema={
            "similarity": "SimilarityResult",
            "budgets": "dict (max_n_llm_evaluations_per_profile, max_pair_llm_calls)",
            "excluded_pairs": "set[str] | None",
        },
        output_schema="list[CandidatePair]",
        run=select_pairs,
        notes="排除 history 中已暴露的 pair（novelty_window_months）。见 spec/05-boundaries.md §8。",
    )
)

register(
    StageSpec(
        name="score",
        description="LLM 对候选对做双向打分（A→B 和 B→A 分别打分）。",
        input_schema={
            "selected_pairs": "list[CandidatePair]",
            "sections_dict": "dict[user_id → sections]",
            "instruction": "str",
            "prompt_template": "str",
            "llm_wrapper": "LLMWrapper",
            "config": "dict",
        },
        output_schema=_SCORES_SCHEMA,
        run=score_pairs_with_llm,
        notes=(
            "未打分候选保留 embedding 权重，不丢弃（unscored_out 参数）。"
            "缓存按完整 prompt hash（hash_text），禁止用内置 hash()。"
            "见 spec/05-boundaries.md §3, §5。"
        ),
    )
)

register(
    StageSpec(
        name="pre_matrix",
        description="把 PairScore 的方向性分数填入双向偏好矩阵（PrefMatrix）。",
        input_schema={
            "pair_scores": _SCORES_SCHEMA,
            "all_user_ids": "list[str]",
        },
        output_schema="PrefMatrix",
        run=build_pref_matrix,
        notes="纯函数，无副作用。这是 LLM 打分到匹配市场的桥接。",
    )
)

register(
    StageSpec(
        name="match",
        description="NSW / α-SW 全局匹配求解 + envy 公平性检查。",
        input_schema={
            "pref_matrix": "PrefMatrix",
            "matching_config": "dict (b_min, b_max, pool_b_max)",
            "blending_config": "dict (embed_weight, llm_weight)",
            "reference_scores": "np.ndarray | None (归一化参考分布)",
        },
        output_schema="(list[Edge], match_prob, envy_report)",
        run=solve_match,
        notes=(
            "NSW 匹配求解 + envy 公平性检查（纯 numpy，无 FairRec/cvxpy/torch 依赖）。"
            "度约束绑定 member 侧；pool_b_max 可选绑定 pool 侧。"
            "见 spec/05-boundaries.md §7。"
        ),
        extra={"envy_gate": {"total_envy_max": 2}},
    )
)

register(
    StageSpec(
        name="introduce",
        description="为每对匹配生成双向对接话术 + 破冰话题。",
        input_schema={
            "edges": _EDGES_SCHEMA,
            "sections_dict": "dict",
            "instruction": "str",
            "prompt_template": "str",
            "llm_wrapper": "LLMWrapper",
        },
        output_schema="dict[pair_id → Introduction]",
        run=generate_introductions_for_matches,
        notes="LLM 失败时 attach_fallback_intro 生成模板话术。",
    )
)

register(
    StageSpec(
        name="report",
        description="生成人类可读的匹配报告。",
        input_schema={
            "edges": _EDGES_SCHEMA,
            "extracted": _SECTIONS_SCHEMA,
            "top_matches_per_user": "int",
            "scope_user_ids": "list[str] | None",
        },
        output_schema="dict (用户报告 + 群组摘要)",
        run=create_report,
        notes="纯函数。scope_user_ids 限定报告范围（batch 模式只报 member 侧）。",
    )
)

register(
    StageSpec(
        name="evaluate",
        description="计算 HR@1/3/5、NDCG@5（推荐质量）+ envy 计数（互惠公平）。",
        input_schema={
            "predictions": "list[list[str]]",
            "ground_truth": "list[str]",
            "pref_matrix": "PrefMatrix | None",
            "match_prob": "np.ndarray | None",
        },
        output_schema="EvaluationReport",
        run=evaluate,
        notes=(
            "计算 HR@1/3/5、NDCG@5（推荐质量）+ envy 计数（互惠公平）。"
            "门禁：hr_at_3 >= 0.6, ndcg_at_5 >= 0.4, total_envy <= 2。"
            "见 spec/03-oracles.md。"
        ),
        extra={"gates": {"hr_at_3_min": 0.6, "ndcg_at_5_min": 0.4, "total_envy_max": 2}},
    )
)
