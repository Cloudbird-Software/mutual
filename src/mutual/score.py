"""Mutual — LLM 双向打分。

对应 docs/engineering-plan.md §3.6、spec/02-stages.md §6。

LLM 对候选对做 **双向** 打分（A→B 和 B→A 分别打分），方向性不盲目对称化。
``score_pairs_with_llm`` 是纯变换。

边界（spec/05-boundaries.md）：
- §3 未打分候选（预算耗尽 / 批次失败）保留 embedding-only 权重，
  通过 ``unscored_out`` 报告，**不静默丢弃**。
- §5 缓存按完整 prompt 的 ``hash_text``，禁止内置 ``hash()``。
- 归一化：``reference`` 分布驱动跨批次稳定归一化。
"""

from __future__ import annotations

import json
from dataclasses import replace
from typing import Any, Dict, List, Optional, Tuple

import numpy as np

from .schemas import CandidatePair, ExtractedSections, PairScore, PrefMatrix


def score_pairs_with_llm(
    selected_pairs: List[CandidatePair],
    sections_dict: Dict[str, Dict[str, str]],
    instruction: str,
    prompt_template: str,
    llm_wrapper: Any,
    config: Dict[str, Any],
    unscored_out: Optional[List[CandidatePair]] = None,
    **kwargs: Any,
) -> Dict[str, PairScore]:
    """LLM 对候选对做双向打分（A→B 和 B→A 分别打分）。

    所有可调参数从 ``config`` 读取，不硬编码：
    - ``budgets.max_n_llm_evaluations_per_profile``：每用户最多打多少对。
    - ``budgets.max_pair_llm_calls``：全局 LLM 调用上限。
    - ``budgets.n_profiles_to_score_together``：一次 prompt 打几对。

    Args:
        selected_pairs: 候选对列表（来自 select 阶段）。
        sections_dict: ``dict[user_id → sections]``（由 :func:`create_sections_dict` 构造）。
        instruction: 打分指令（``recipe.instruction``）。
        prompt_template: 打分 prompt 模板。
        llm_wrapper: :class:`~mutual.llm.LLMWrapper` 实例（鸭子类型）。
        config: 配置 dict。
        unscored_out: 可选 out-param；调用方传入 list，本函数向其 append
            未打分的候选（预算耗尽 / 批次失败）。这些候选保留 embedding 权重（§3）。
        **kwargs: 预留扩展（如 ``reference_scores``）。

    Returns:
        ``dict[pair_id → PairScore]``。``PairScore.llm_score_a_to_b`` /
        ``llm_score_b_to_a`` 为方向性 LLM 分；未打分者对应字段为 ``None``。

    边界：
    - 未打分候选保留 embedding 权重，不丢弃（§3）。
    - 缓存按完整 prompt hash（``hash_text``），禁止内置 ``hash()``（§5）。
    """
    budgets = dict(config.get("budgets") or {})
    per_profile_cap = budgets.get("max_n_llm_evaluations_per_profile")
    max_calls = budgets.get("max_pair_llm_calls")
    try:
        batch_size = int(budgets.get("n_profiles_to_score_together") or 1)
    except (TypeError, ValueError):
        batch_size = 1
    batch_size = max(1, batch_size)
    model = (config.get("models") or {}).get("pair_llm")

    # 去重（同 pair_id 保留首个），保持 select 阶段的顺序 → 结果确定性。
    unique_pairs: Dict[str, CandidatePair] = {}
    for pair in selected_pairs:
        unique_pairs.setdefault(pair.pair_id, pair)
    pairs = list(unique_pairs.values())

    # 每用户预算（spec/02-stages.md §6）：预算不足的 pair 直接记为未打分。
    evals_per_user: Dict[str, int] = {}
    unscored_ids: set[str] = set()
    admissible: List[CandidatePair] = []
    for pair in pairs:
        if per_profile_cap is not None and (
            evals_per_user.get(pair.user1, 0) >= per_profile_cap
            or evals_per_user.get(pair.user2, 0) >= per_profile_cap
        ):
            unscored_ids.add(pair.pair_id)
            continue
        admissible.append(pair)
        evals_per_user[pair.user1] = evals_per_user.get(pair.user1, 0) + 1
        evals_per_user[pair.user2] = evals_per_user.get(pair.user2, 0) + 1

    batches = [admissible[i : i + batch_size] for i in range(0, len(admissible), batch_size)]
    scored: Dict[str, Tuple[float, float]] = {}
    calls_made = 0
    for batch in batches:
        if max_calls is not None and calls_made >= max_calls:
            unscored_ids.update(pair.pair_id for pair in batch)
            continue
        prompt = _build_scoring_prompt(batch, sections_dict, instruction, prompt_template)
        messages: List[Dict[str, str]] = [{"role": "user", "content": prompt}]
        call_kwargs: Dict[str, Any] = {}
        if model:
            call_kwargs["model"] = model
        try:
            raw = llm_wrapper(messages, **call_kwargs)
        except Exception:
            raw = None
        calls_made += 1
        parsed = _parse_scoring_response(raw, expected_pairs=len(batch)) if raw else []
        for idx, pair in enumerate(batch):
            got = parsed[idx] if idx < len(parsed) else None
            if got is None:
                unscored_ids.add(pair.pair_id)
            else:
                scored[pair.pair_id] = got

    results: Dict[str, PairScore] = {}
    for pair in pairs:
        a_to_b = b_to_a = None
        if pair.pair_id in scored:
            a_to_b, b_to_a = scored[pair.pair_id]
        results[pair.pair_id] = PairScore(
            pair_id=pair.pair_id,
            user1=pair.user1,
            user2=pair.user2,
            embed_score=float(pair.similarity_score),
            llm_score=_fuse_directional(a_to_b, b_to_a),
            llm_score_a_to_b=a_to_b,
            llm_score_b_to_a=b_to_a,
        )

    if unscored_out is not None:
        unscored_out.extend(pair for pair in pairs if pair.pair_id in unscored_ids)
    return results


def create_sections_dict(
    extracted: List[ExtractedSections],
) -> Dict[str, Dict[str, str]]:
    """把 ``list[ExtractedSections]`` 转成 ``dict[user_id → sections]``。

    供 score / introduce 阶段按 user_id 查 profile 内容，避免线性扫描。

    Args:
        extracted: ``list[ExtractedSections]``。

    Returns:
        ``dict[user_id → {section: text, ...}]``。
    """
    return {es.id: dict(es.sections) for es in extracted}


def prepare_normalized_scores(
    scores: Dict[str, PairScore],
    reference: Optional[np.ndarray] = None,
) -> Dict[str, PairScore]:
    """跨批次稳定归一化 embed/llm 分数。

    以 ``reference`` 分布为基准归一化，使不同批次（query / batch 模式）
    的分数可比。``reference=None`` 时用当前批次的统计量。

    Args:
        scores: ``dict[pair_id → PairScore]``。
        reference: 参考分数分布（``np.ndarray``）；``None`` 用当前批次。

    Returns:
        更新了 ``embed_score_normalized`` / ``llm_score_normalized`` 字段的
        scores（副本或原地由实现决定）。
    """
    if not scores:
        return {}

    embed_vals = [float(ps.embed_score) for ps in scores.values() if ps.embed_score is not None]
    llm_vals = [float(ps.llm_score) for ps in scores.values() if ps.llm_score is not None]

    # ``reference`` 单一数组解释为 embed 分数的参考分布（spec 沉默 S2）；
    # 也接受 ``{"embed": arr, "llm": arr}`` 形式的分分量参考。
    ref_embed: Optional[np.ndarray] = None
    ref_llm: Optional[np.ndarray] = None
    if isinstance(reference, dict):
        ref_embed = reference.get("embed")
        ref_llm = reference.get("llm")
    elif reference is not None and len(reference) > 0:
        ref_embed = reference

    out: Dict[str, PairScore] = {}
    for pair_id, ps in scores.items():
        embed_norm = _minmax_normalize(ps.embed_score, embed_vals, ref_embed)
        llm_norm = (
            _minmax_normalize(ps.llm_score, llm_vals, ref_llm) if ps.llm_score is not None else None
        )
        out[pair_id] = replace(ps, embed_score_normalized=embed_norm, llm_score_normalized=llm_norm)
    return out


def build_pref_matrix(
    pair_scores: Dict[str, PairScore],
    all_user_ids: List[str],
) -> PrefMatrix:
    """把 ``PairScore`` 的方向性分数填入双向偏好矩阵（spec/02-stages.md §7）。

    填充规则（docs/engineering-plan.md §4.2）：
    - ``llm_score_a_to_b`` → ``pref_left_to_right[i, j]``（user1 在左、user2 在右）；
    - ``llm_score_b_to_a`` → ``pref_right_to_left[j, i]``；
    - 互补单元格（同一无序对的另一方向）由同一对的相反方向分数填充，
      使两个矩阵覆盖全部有序对；
    - 缺失的 LLM 分数用 ``embed_score`` 兜底（embedding 无方向 → 双向同值）；
    - 无 PairScore 的对与对角线填 0.0。

    Args:
        pair_scores: ``dict[pair_id → PairScore]``（含未打分候选，§3）。
        all_user_ids: 全部用户 ID（左右两侧同序）。

    Returns:
        :class:`~mutual.schemas.PrefMatrix`（N×N，left_ids = right_ids）。
    """
    ids: List[str] = []
    seen: set[str] = set()
    for uid in all_user_ids:
        if uid not in seen:
            seen.add(uid)
            ids.append(uid)
    index = {uid: i for i, uid in enumerate(ids)}
    n = len(ids)
    pref_lr = np.zeros((n, n), dtype=float)
    pref_rl = np.zeros((n, n), dtype=float)

    for ps in pair_scores.values():
        i = index.get(ps.user1)
        j = index.get(ps.user2)
        if i is None or j is None or i == j:
            continue
        a_val = ps.llm_score_a_to_b if ps.llm_score_a_to_b is not None else ps.embed_score
        b_val = ps.llm_score_b_to_a if ps.llm_score_b_to_a is not None else ps.embed_score
        pref_lr[i, j] = a_val
        pref_lr[j, i] = b_val
        pref_rl[j, i] = b_val
        pref_rl[i, j] = a_val

    return PrefMatrix(
        left_ids=ids,
        right_ids=list(ids),
        pref_left_to_right=pref_lr,
        pref_right_to_left=pref_rl,
    )


def build_bipartite_pref_matrix(
    pair_scores: Dict[str, PairScore],
    left_ids: List[str],
    right_ids: List[str],
) -> PrefMatrix:
    """构建 member×pool 二部图偏好矩阵（batch/query 模式，spec/05-boundaries.md §7）。

    与 :func:`build_pref_matrix`（同集方阵）相对：``left_ids`` 为 member
    （source）侧，``right_ids`` 为 pool（target）侧，矩阵为 M×N 矩形。
    度约束 ``b_max`` 由 match 阶段绑定 member（左）侧、``pool_b_max``
    绑定 pool（右）侧。

    填充规则：
    - ``PairScore.user1`` 在左侧、``user2`` 在右侧：
      ``pref_lr[i, j] = a_to_b``、``pref_rl[j, i] = b_to_a``；
    - 反向（user1 在 pool 侧、user2 在 member 侧）按分数方向对调填入；
    - 缺失的 LLM 分数用 ``embed_score`` 兜底；左右同 id（member∈pool 的
      自配对）不填。

    Args:
        pair_scores: ``dict[pair_id → PairScore]``。
        left_ids: member 侧全部 ID（去重保序）。
        right_ids: pool 侧全部 ID（去重保序）。

    Returns:
        :class:`~mutual.schemas.PrefMatrix`（M×N 矩形）。
    """
    left_index = _index_unique(left_ids)
    right_index = _index_unique(right_ids)
    m, n = len(left_index), len(right_index)
    pref_lr = np.zeros((m, n), dtype=float)
    pref_rl = np.zeros((n, m), dtype=float)

    for ps in pair_scores.values():
        a_val = ps.llm_score_a_to_b if ps.llm_score_a_to_b is not None else ps.embed_score
        b_val = ps.llm_score_b_to_a if ps.llm_score_b_to_a is not None else ps.embed_score
        i, j = left_index.get(ps.user1), right_index.get(ps.user2)
        if i is not None and j is not None:
            if ps.user1 == ps.user2:
                continue
            pref_lr[i, j] = a_val
            pref_rl[j, i] = b_val
            continue
        i, j = left_index.get(ps.user2), right_index.get(ps.user1)
        if i is not None and j is not None:
            if ps.user1 == ps.user2:
                continue
            pref_lr[i, j] = b_val
            pref_rl[j, i] = a_val

    return PrefMatrix(
        left_ids=list(left_index),
        right_ids=list(right_index),
        pref_left_to_right=pref_lr,
        pref_right_to_left=pref_rl,
    )


# ---------------------------------------------------------------------------
# 内部 helper（纯函数）
# ---------------------------------------------------------------------------


def _index_unique(ids: List[str]) -> Dict[str, int]:
    """去重保序地建立 id → 索引映射。"""
    index: Dict[str, int] = {}
    for uid in ids:
        if uid not in index:
            index[uid] = len(index)
    return index


def _fuse_directional(a_to_b: Optional[float], b_to_a: Optional[float]) -> Optional[float]:
    """融合双向分数：取已有方向的算术平均（spec 未规定公式，见沉默 S3）。"""
    vals = [v for v in (a_to_b, b_to_a) if v is not None]
    if not vals:
        return None
    return sum(vals) / len(vals)


def _minmax_normalize(
    value: Optional[float],
    batch_values: List[float],
    reference: Optional[np.ndarray],
) -> float:
    """min-max 归一化；优先用 reference 分布的 min/max，否则用当前批次。

    reference 令不同批次（query/batch 模式）的分数在同一尺度上可比；
    退化情形（max == min）返回 0.5 中性值（沉默 S4）。
    """
    if value is None:
        return 0.5
    vals: Any = batch_values
    if reference is not None and len(reference) > 0:
        vals = reference
    if len(vals) == 0:
        return 0.5
    lo = float(np.min(vals))
    hi = float(np.max(vals))
    if hi <= lo:
        return 0.5
    normalized = (float(value) - lo) / (hi - lo)
    return float(min(1.0, max(0.0, normalized)))


class _MissingKeyDict(dict):
    """format_map 用：缺失占位符渲染为空串，模板缺失时不崩。"""

    def __missing__(self, key: str) -> str:
        return ""


def _format_sections(sections: Optional[Dict[str, str]]) -> str:
    if not sections:
        return "Not specified"
    return "\n".join(f"{k}: {v}" for k, v in sorted(sections.items()))


def _safe_format(template: str, mapping: Dict[str, str]) -> str:
    try:
        return template.format_map(_MissingKeyDict(mapping))
    except (ValueError, IndexError):
        return template


def _build_scoring_prompt(
    batch: List[CandidatePair],
    sections_dict: Dict[str, Dict[str, str]],
    instruction: str,
    prompt_template: str,
) -> str:
    """构造批量打分 prompt：一次打 ``len(batch)`` 对（预算 n_profiles_to_score_together）。

    打分类 prompt 必含输出格式标记 ``a_to_b``（fake_llm 路由规则，
    spec/04-fixtures.md §7.1）。批量（>1 对）时要求 JSON 数组响应；
    单对时沿用模板的单对象 JSON 请求。
    """
    blocks = []
    for idx, pair in enumerate(batch, start=1):
        rendered = _safe_format(
            prompt_template,
            {
                "user1_sections": _format_sections(sections_dict.get(pair.user1)),
                "user2_sections": _format_sections(sections_dict.get(pair.user2)),
                "instruction": instruction,
                "user1": pair.user1,
                "user2": pair.user2,
            },
        )
        blocks.append(f"### Pair {idx}: ({pair.user1}, {pair.user2})\n{rendered}")
    if len(batch) == 1:
        return blocks[0]
    header = (
        f"Score each of the {len(batch)} pairs below, in both directions. "
        "Respond ONLY with a JSON array of exactly "
        f"{len(batch)} objects, in order, each of the form "
        '{{"a_to_b": <float 0.0-1.0>, "b_to_a": <float 0.0-1.0>, "reasoning": "<brief>"}}.'
    )
    return header + "\n\n" + "\n\n".join(blocks)


def _loads_lenient(text: str) -> Any:
    """容忍 markdown 代码围栏与前后噪声的 json.loads。"""
    s = text.strip()
    if s.startswith("```"):
        s = s.split("\n", 1)[1] if "\n" in s else s
        s = s.rstrip()
        if s.endswith("```"):
            s = s[:-3].rstrip()
    try:
        return json.loads(s)
    except json.JSONDecodeError:
        pass
    for open_ch, close_ch in (("[", "]"), ("{", "}")):
        start = s.find(open_ch)
        end = s.rfind(close_ch)
        if start != -1 and end > start:
            try:
                return json.loads(s[start : end + 1])
            except json.JSONDecodeError:
                continue
    return None


def _clamp01(value: Any) -> Optional[float]:
    if isinstance(value, bool):
        return None
    try:
        f = float(value)
    except (TypeError, ValueError):
        return None
    if f != f:  # NaN
        return None
    return max(0.0, min(1.0, f))


def _parse_scoring_response(text: str, expected_pairs: int) -> List[Optional[Tuple[float, float]]]:
    """解析 LLM 打分响应 → 与 batch 按序对齐的 ``(a_to_b, b_to_a)`` 列表。

    单对 batch 接受单 JSON 对象（或数组首元素）；多对 batch 只接受 JSON 数组，
    单对象视为格式不符（整批未打分，保留 embed 权重，§3）。
    每个对象必须同时含合法的 ``a_to_b`` 与 ``b_to_a``（0.0-1.0，截断处理）。

    位置对齐（qodo #3）：非对象元素**保留为 ``None`` 槽位**，不压缩列表——
    否则畸形元素之后的合法分数会左移，被记到错误的 pair 头上。
    """
    obj = _loads_lenient(text)
    if isinstance(obj, list):
        items: List[Optional[Dict[str, Any]]] = [o if isinstance(o, dict) else None for o in obj]
    elif isinstance(obj, dict):
        if expected_pairs > 1:
            return []
        items = [obj]
    else:
        return []
    if expected_pairs == 1 and len(items) > 1:
        items = items[:1]

    out: List[Optional[Tuple[float, float]]] = []
    for d in items:
        if d is None:
            out.append(None)
            continue
        a = _clamp01(d.get("a_to_b"))
        b = _clamp01(d.get("b_to_a"))
        out.append((a, b) if (a is not None and b is not None) else None)
    return out
