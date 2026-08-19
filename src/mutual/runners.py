"""Mutual — 模式运行器。

对应 docs/engineering-plan.md §3.8。

三种运行模式：
- :func:`run_full_match` — N×N 全量匹配。
- :func:`run_query_match` — 1×M 查询匹配（单查询对候选池）。
- :func:`run_batch_match` — M×N 子集批量匹配（互惠推荐主模式）。

约定：
- schema in, schema out（输入输出均为 :mod:`mutual.schemas` 中的 dataclass）。
- ``store=None`` 时全内存运行；传入 Store 时做持久化。
- 串联 pipeline：
  ``extract → hyde → embed → similarity → select → score → pre_matrix
  → match → introduce → report``。

IO 边界（CLAUDE.md §2.3）：core 阶段是纯变换，IO 只在此处与
:mod:`mutual.store` 中发生。runners 是 adapter，负责把纯变换串成流程。
"""

from __future__ import annotations

from dataclasses import dataclass, field, replace
from typing import Any, Dict, List, Optional, Set, Tuple, Union

from . import introduce as introduce_mod
from . import report as report_mod
from . import score as score_mod
from . import stages
from .config import resolve_prompt_templates
from .schemas import (
    CandidatePair,
    Edge,
    EmbeddingsBundle,
    ExtractedSections,
    MatchResult,
    PrefMatrix,
    Profile,
)
from .store import Store


@dataclass
class BatchMatchResult:
    """:func:`run_batch_match` 的输出。

    包装 :class:`~mutual.schemas.MatchResult`，并附带 batch 模式专属元数据：
    member 侧 / pool 侧 ID、被 novelty 排除的 pair_id、以及运行元信息。
    """

    match_result: MatchResult
    member_ids: List[str]
    pool_ids: List[str] = field(default_factory=list)
    excluded_pair_ids: List[str] = field(default_factory=list)
    metadata: Dict[str, Any] = field(default_factory=dict)


def run_full_match(
    profiles_or_bundle: Union[List[Profile], EmbeddingsBundle],
    config: Dict[str, Any],
    store: Optional[Store] = None,
    **kwargs: Any,
) -> MatchResult:
    """N×N 全量匹配。

    传入 ``list[Profile]`` 时从头跑 extract→…→report；
    传入已有 ``EmbeddingsBundle`` 时跳过 extract/hyde/embed，从 similarity 起跑。

    Args:
        profiles_or_bundle: 原始 profiles 或已有 EmbeddingsBundle。
        config: 完整配置 dict（所有可调参数从此读取，不硬编码）。
        store: 可选 Store；``None`` 时全内存运行，不落盘。
        **kwargs: 透传给各 stage（如 ``llm_wrapper``、``excluded_pairs``）。

    Returns:
        :class:`~mutual.schemas.MatchResult`。
    """
    llm_wrapper = _require_llm_wrapper(kwargs)
    matching_config: Dict[str, Any] = dict(config.get("matching") or {})

    excluded_pairs = _resolve_excluded_pairs(kwargs, store)

    if isinstance(profiles_or_bundle, EmbeddingsBundle):
        bundle = profiles_or_bundle
        extracted = _bundle_sections(bundle.user_ids, kwargs, store)
    else:
        profiles = list(profiles_or_bundle)
        min_required = int(matching_config.get("min_profiles_required", 0) or 0)
        if len(profiles) < min_required:
            raise ValueError(
                f"profile 数 {len(profiles)} 低于 matching.min_profiles_required="
                f"{min_required}，拒绝运行"
            )
        extracted, failed_ids = _run_extract_stage(profiles, config, llm_wrapper)
        if store is not None:
            store.put_sections([es for es in extracted if es.id not in failed_ids])
        hyde = stages.get_stage("hyde").run(
            sections=extracted, config=config, llm_wrapper=llm_wrapper
        )
        existing = store.get_embeddings() if store is not None else kwargs.get("existing_bundle")
        # qodo #8：embed 阶段经 config["llm_wrapper"] 解析 embedder（A-2），
        # 此前 wrapper 在 runner 手里却没传下去 → 正常调用路径 raise。
        embed_config = dict(config)
        embed_config["llm_wrapper"] = llm_wrapper
        bundle = stages.get_stage("embed").run(
            sections=extracted, hyde=hyde, config=embed_config, existing=existing
        )
        if store is not None:
            store.put_embeddings(bundle)

    match_result, _meta = _run_match_flow(
        source_bundle=bundle,
        target_bundle=None,
        source_extracted=extracted,
        pool_extracted=[],
        config=config,
        llm_wrapper=llm_wrapper,
        excluded_pairs=excluded_pairs,
        scope_user_ids=None,
        reference_scores=kwargs.get("reference_scores"),
        top_matches_per_user=_resolve_top_matches_per_user(config, kwargs),
    )
    if store is not None:
        store.put_matches(match_result.edges)
    return match_result


def run_query_match(
    query_text: str,
    pool_bundle: EmbeddingsBundle,
    config: Dict[str, Any],
    **kwargs: Any,
) -> MatchResult:
    """1×M 查询匹配：把 ``query_text`` 当作单用户与 pool 做匹配。

    query 经 extract/hyde/embed 后与 ``pool_bundle`` 做 M×N 相似度（M=1），
    再 select/score/match（度约束退化为单侧）/introduce/report。

    Args:
        query_text: 查询自由文本。
        pool_bundle: 候选池的 EmbeddingsBundle。
        config: 完整配置 dict。
        **kwargs: 透传（如 ``llm_wrapper``）。

    Returns:
        :class:`~mutual.schemas.MatchResult`。
    """
    llm_wrapper = _require_llm_wrapper(kwargs)
    query_id = str(kwargs.get("query_id", "query"))
    pool_sections = _pool_sections(kwargs)

    # query 文本广播到全部 section 名，保证 query bundle 与 pool 的
    # section_names 对齐（spec 沉默 S5，见模块内注释）。
    query_profile = Profile(
        id=query_id,
        sections={name: query_text for name in pool_bundle.section_names},
    )
    extracted, _failed_ids = _run_extract_stage([query_profile], config, llm_wrapper)
    hyde = stages.get_stage("hyde").run(sections=extracted, config=config, llm_wrapper=llm_wrapper)
    # qodo #8：同 run_full_match——把 wrapper 传给 embed 阶段解析 embedder。
    embed_config = dict(config)
    embed_config["llm_wrapper"] = llm_wrapper
    query_bundle = stages.get_stage("embed").run(
        sections=extracted, hyde=hyde, config=embed_config, existing=None
    )

    match_result, _meta = _run_match_flow(
        source_bundle=query_bundle,
        target_bundle=pool_bundle,
        source_extracted=extracted,
        pool_extracted=pool_sections,
        config=config,
        llm_wrapper=llm_wrapper,
        excluded_pairs=_resolve_excluded_pairs(kwargs, None),
        scope_user_ids=[query_id],
        reference_scores=kwargs.get("reference_scores"),
        top_matches_per_user=_resolve_top_matches_per_user(config, kwargs),
    )
    return match_result


def run_batch_match(
    member_ids: List[str],
    pool_bundle: EmbeddingsBundle,
    config: Dict[str, Any],
    excluded_pairs: Optional[Set[str]] = None,
    **kwargs: Any,
) -> BatchMatchResult:
    """M×N 子集批量匹配（互惠推荐主模式）。

    member 侧（``member_ids``）从 ``pool_bundle`` 取子集，与整个 pool 做 M×N
    相似度；度约束 ``b_min``/``b_max`` 绑定 member 侧（spec/05-boundaries.md §7）。
    报告范围限定在 member 侧（``scope_user_ids``）。

    Args:
        member_ids: 主动匹配的 member 侧 ID（``pool_bundle.user_ids`` 的子集）。
        pool_bundle: 候选池的 EmbeddingsBundle。
        config: 完整配置 dict（``matching``、``blending``、``budgets`` 等）。
        excluded_pairs: novelty 排除的 pair_id 集合（来自 ``match_history``，§8）。
        **kwargs: 透传（如 ``llm_wrapper``）。

    Returns:
        :class:`BatchMatchResult`（内含 :class:`~mutual.schemas.MatchResult`）。
    """
    llm_wrapper = _require_llm_wrapper(kwargs)
    member_ids = list(member_ids)
    pool_sections = _pool_sections(kwargs)
    member_set = set(member_ids)
    member_extracted = [es for es in pool_sections if es.id in member_set]

    member_bundle = pool_bundle.subset(member_ids)

    match_result, meta = _run_match_flow(
        source_bundle=member_bundle,
        target_bundle=pool_bundle,
        source_extracted=member_extracted,
        pool_extracted=pool_sections,
        config=config,
        llm_wrapper=llm_wrapper,
        excluded_pairs=excluded_pairs,
        scope_user_ids=member_ids,
        reference_scores=kwargs.get("reference_scores"),
        top_matches_per_user=_resolve_top_matches_per_user(config, kwargs),
    )
    return BatchMatchResult(
        match_result=match_result,
        member_ids=member_ids,
        pool_ids=list(pool_bundle.user_ids),
        excluded_pair_ids=sorted(excluded_pairs) if excluded_pairs else [],
        metadata=meta,
    )


# ---------------------------------------------------------------------------
# 内部 helper：pipeline 串联（similarity → select → score → pre_matrix →
# match（含兜底）→ introduce → report）
# ---------------------------------------------------------------------------


def _run_match_flow(
    source_bundle: EmbeddingsBundle,
    target_bundle: Optional[EmbeddingsBundle],
    source_extracted: List[ExtractedSections],
    pool_extracted: List[ExtractedSections],
    config: Dict[str, Any],
    llm_wrapper: Any,
    excluded_pairs: Optional[Set[str]],
    scope_user_ids: Optional[List[str]],
    reference_scores: Any,
    top_matches_per_user: Optional[int],
) -> Tuple[MatchResult, Dict[str, Any]]:
    """similarity → select → score → pre_matrix → match → introduce → report。

    match 阶段经注册表获取（spec/02-stages.md §8）。
    """
    recipe = dict(config.get("recipe") or {})
    budgets = dict(config.get("budgets") or {})

    similarity = stages.get_stage("similarity").run(
        source=source_bundle, target=target_bundle, recipe_config=recipe
    )
    selected: List[CandidatePair] = stages.get_stage("select").run(
        similarity=similarity, budgets=budgets, excluded_pairs=excluded_pairs
    )

    sections_dict = score_mod.create_sections_dict(source_extracted + pool_extracted)
    templates = resolve_prompt_templates(config)
    unscored: List[CandidatePair] = []
    pair_scores = score_mod.score_pairs_with_llm(
        selected,
        sections_dict,
        instruction=str(recipe.get("instruction", "")),
        prompt_template=templates["scoring"],
        llm_wrapper=llm_wrapper,
        config=config,
        unscored_out=unscored,
    )
    pair_scores = score_mod.prepare_normalized_scores(pair_scores, reference=reference_scores)

    if target_bundle is not None:
        # 二部图模式（query/batch）：member（source）× pool（target）矩形矩阵，
        # b_max 绑定 member 侧、pool_b_max 绑定 pool 侧（spec/05-boundaries.md §7）。
        pref_matrix: PrefMatrix = score_mod.build_bipartite_pref_matrix(
            pair_scores, list(source_bundle.user_ids), list(target_bundle.user_ids)
        )
    else:
        # 同集模式（full）：N×N 方阵（left == right）。
        all_user_ids = list(source_bundle.user_ids)
        pref_matrix = score_mod.build_pref_matrix(pair_scores, all_user_ids)

    edges, envy_report, match_fallback = _run_match_stage(pref_matrix, config, reference_scores)

    introductions = introduce_mod.generate_introductions_for_matches(
        edges,
        sections_dict,
        instruction=str(recipe.get("instruction", "")),
        prompt_template=templates["introduction"],
        llm_wrapper=llm_wrapper,
        model=(config.get("models") or {}).get("pair_llm"),
    )
    edges = [
        replace(
            edge,
            intro=introductions[edge.pair_id].intro,
            starter_topics=introductions[edge.pair_id].starter_topics,
        )
        if edge.pair_id in introductions
        else introduce_mod.attach_fallback_intro(edge)
        for edge in edges
    ]

    extracted_all = _dedupe_extracted(source_extracted + pool_extracted)
    report_data = report_mod.create_report(
        edges, extracted_all, top_matches_per_user or 0, scope_user_ids
    )
    notes: List[str] = report_data.setdefault("notes", [])
    if unscored:
        notes.append(
            f"{len(unscored)} 个候选对因预算/解析失败未获 LLM 打分，保留 embedding 权重"
            "（spec/05-boundaries.md §3）。"
        )

    new_pairs = [{"pair_id": e.pair_id, "user1": e.user1, "user2": e.user2} for e in edges]
    match_result = MatchResult(
        edges=edges,
        report_data=report_data,
        new_pairs=new_pairs,
        envy_report=envy_report,
    )
    meta = {
        "match_fallback": False,
        "n_selected_pairs": len(selected),
        "n_scored_pairs": len(selected) - len(unscored),
        "n_unscored_pairs": len(unscored),
    }
    return match_result, meta


def _run_match_stage(
    pref_matrix: PrefMatrix,
    config: Dict[str, Any],
    reference_scores: Any,
) -> Tuple[List[Edge], Optional[Dict[str, Any]], bool]:
    """经注册表调用 match 阶段（NSW 求解 + envy 检查）。"""
    spec = stages.get_stage("match")
    result = spec.run(
        pref_matrix=pref_matrix,
        matching_config=dict(config.get("matching") or {}),
        blending_config=dict(config.get("blending") or {}),
        reference_scores=reference_scores,
    )
    edges, _match_prob, envy_report = result[0], result[1], result[2]
    return list(edges), envy_report, False


def _run_extract_stage(
    profiles: List[Profile], config: Dict[str, Any], llm_wrapper: Any
) -> Tuple[List[ExtractedSections], Set[str]]:
    """extract 阶段（经注册表）；返回 (extracted, failed_ids)。

    失败项填 "Not specified" 继续跑 pipeline，但持久化时被排除
    （spec/05-boundaries.md §4，由 caller/store 处理）。
    """
    failed: List[str] = []
    extracted = list(
        stages.get_stage("extract").run(
            profiles=profiles, config=config, llm_wrapper=llm_wrapper, failed_out=failed
        )
    )
    return extracted, set(failed)


def _require_llm_wrapper(kwargs: Dict[str, Any]) -> Any:
    llm_wrapper = kwargs.get("llm_wrapper")
    if llm_wrapper is None:
        raise ValueError(
            "llm_wrapper 是 LLM 阶段（extract/hyde/score/introduce）的必需参数，"
            "请经 kwargs 注入（鸭子类型）。"
        )
    return llm_wrapper


def _resolve_excluded_pairs(kwargs: Dict[str, Any], store: Optional[Store]) -> Optional[Set[str]]:
    """novelty 排除集：kwargs 显式传入优先，否则从 store 的 match_history 构建（§8）。"""
    explicit = kwargs.get("excluded_pairs")
    if explicit is not None:
        return set(explicit)
    if store is not None:
        history = store.get_match_history()
        return {rec["pair_id"] for rec in history if "pair_id" in rec}
    return None


def _bundle_sections(
    user_ids: List[str], kwargs: Dict[str, Any], store: Optional[Store]
) -> List[ExtractedSections]:
    """bundle 直入模式：sections 从 store 或 kwargs 取（打分/话术 prompt 需要）。"""
    if store is not None:
        by_id = store.get_sections(user_ids)
        return [by_id[uid] for uid in user_ids if uid in by_id]
    for key in ("extracted", "sections"):
        candidate = kwargs.get(key)
        if candidate:
            wanted = set(user_ids)
            return [es for es in candidate if getattr(es, "id", None) in wanted]
    return []


def _pool_sections(kwargs: Dict[str, Any]) -> List[ExtractedSections]:
    """query/batch 模式：pool 侧 sections 经 kwargs 注入（签名无 store 参数）。"""
    for key in ("pool_sections", "extracted", "sections"):
        candidate = kwargs.get(key)
        if candidate:
            return list(candidate)
    return []


def _dedupe_extracted(extracted: List[ExtractedSections]) -> List[ExtractedSections]:
    seen: Set[str] = set()
    out: List[ExtractedSections] = []
    for es in extracted:
        if es.id not in seen:
            seen.add(es.id)
            out.append(es)
    return out


def _resolve_top_matches_per_user(config: Dict[str, Any], kwargs: Dict[str, Any]) -> Optional[int]:
    """每用户报告条数：kwargs > config（reporting 段，config/default.yaml 暂缺，S6）。"""
    value = kwargs.get("top_matches_per_user")
    if value is None:
        value = (config.get("reporting") or {}).get("top_matches_per_user")
    if value is None:
        return None
    return int(value)
