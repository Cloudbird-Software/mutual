"""Mutual — 方向性相似度。

对应 docs/engineering-plan.md §3.6、spec/02-stages.md §4。

计算方向性相似度矩阵（rectangular M×N 或 square N×N），
``compute_similarity`` 是纯函数，无副作用。

边界（spec/05-boundaries.md §1、§2）：
- 缺失 section = 中性，不是零：零范数向量被 mask，融合分母只算
  实际存在（双侧均有效）的 term 权重（分母修正）。
- 方向性不盲目对称化：``needs_skills`` 等 cross term 是方向性的
  （source 的 needs ↔ target 的 skills），``dir_matrix[i,j] ≠ dir_matrix[j,i]``。
- 例外：``target=None`` 的 N×N 方阵 legacy 路径对 ``dir_matrix`` 做
  ``(dir + dir.T) / 2`` 对称化（仅为旧代码 bit-exact 兼容）。

spec 沉默：
- A-4：``dir_matrix`` / ``fused_matrix`` 的精确分工 spec 未细化。实现：
  ``dir_matrix`` = 含方向性 cross term 的加权融合分（mask + 分母修正）；
  ``fused_matrix`` = square 模式 ``(dir + dir.T)/2``，rect 模式 = ``dir``
  原样（下游 select 以 ``fused_matrix`` 为选择依据）。
- A-5：HyDE 向量参与相似度的方式（stages.py hyde notes 只说
  "max-pool over descriptor pairs"）。实现：每 section 的候选集 =
  section 原始向量 + 全部 HyDE 描述符向量，跨侧两两 cosine 取 max。
- A-6：M×N 模式两侧 ``section_names`` 不一致时取交集（按 source 顺序），
  bundle 中未配置 ``recipe.section_weights`` 权重的 section 不参与融合。
"""

from __future__ import annotations

from typing import Any, Dict, Optional, Tuple

import numpy as np

from .schemas import EmbeddingsBundle, SimilarityResult

_EPS = 1e-12


def _candidate_matrix(bundle: EmbeddingsBundle, section_index: int, name: str):
    """取某 section 的候选向量矩阵（section 原向量 + HyDE 描述符向量）。

    Returns:
        ``(unit, valid)``：``unit`` 形状 ``[N, C, D]``（已归一化），
        ``valid`` 形状 ``[N, C]``（零范数候选 = 缺失，无效）。
    """
    mats = [np.asarray(bundle.embeddings[:, section_index : section_index + 1, :], dtype=float)]
    hyde = bundle.hyde.get(name)
    if hyde is not None and getattr(hyde, "ndim", 0) == 3 and hyde.shape[1] > 0:
        mats.append(np.asarray(hyde, dtype=float))
    cand = np.concatenate(mats, axis=1)
    norms = np.linalg.norm(cand, axis=-1)
    valid = norms > _EPS
    unit = cand / np.where(valid, norms, 1.0)[..., None]
    return unit, valid


def _pooled_similarity(src_unit, src_valid, tgt_unit, tgt_valid):
    """跨侧候选对 max-pool（A-5），并返回双侧均有有效候选的 mask。"""
    dots = np.einsum("icd,jkd->ijck", src_unit, tgt_unit)
    ok = src_valid[:, None, :, None] & tgt_valid[None, :, None, :]
    pooled = np.where(ok, dots, -np.inf).max(axis=(2, 3))
    any_ok = ok.any(axis=(2, 3))
    return np.where(any_ok, pooled, 0.0), any_ok


def _cross_sections(key: str) -> Optional[Tuple[str, str]]:
    """解析 cross 权重键 ``"X_Y"`` → ``(X, Y)``（source 的 X ↔ target 的 Y）。"""
    if "_" not in key:
        return None
    x, y = key.split("_", 1)
    return x, y


def compute_similarity(
    source: EmbeddingsBundle,
    target: Optional[EmbeddingsBundle],
    recipe_config: Dict[str, Any],
) -> SimilarityResult:
    """计算方向性相似度矩阵；``target=None`` 时为 N×N 方阵模式。

    融合公式（mask + 分母修正，spec/05-boundaries.md §1）::

        dir[i,j] = (Σ_t w_t · cos_t[i,j]) / (Σ_t w_t)
                   （t 只计双侧该 term 均有效的项；无有效项 → 0）

    term 分两类：
    - section 自身项：``section_weights[s] · cos(src_s[i], tgt_s[j])``；
    - 方向性 cross 项：``cross_section_weights["X_Y"] · cos(src_X[i], tgt_Y[j])``
      （如 ``needs_skills`` = A 的 needs ↔ B 的 skills，不对称化）。

    Args:
        source: 源侧 bundle（M members）。
        target: 目标侧 bundle（N pool）；``None`` 表示 N×N 方阵模式
            （source 对自身，legacy ``(dir+dir.T)/2`` 对称化，A-4）。
        recipe_config: recipe 配置（``section_weights``、
            ``cross_section_weights``）。

    Returns:
        :class:`~mutual.schemas.SimilarityResult`。
    """
    square = target is None
    tgt = source if target is None else target

    weights = {str(k): float(v) for k, v in (recipe_config.get("section_weights") or {}).items()}
    cross_weights = {
        str(k): float(v) for k, v in (recipe_config.get("cross_section_weights") or {}).items()
    }

    src_index = {n: k for k, n in enumerate(source.section_names)}
    tgt_index = {n: k for k, n in enumerate(tgt.section_names)}
    names = [n for n in source.section_names if n in tgt_index]

    cache: Dict[Tuple[int, str], Any] = {}

    def _side(bundle: EmbeddingsBundle, index_map: Dict[str, int], name: str):
        key = (id(bundle), name)
        if key not in cache:
            cache[key] = _candidate_matrix(bundle, index_map[name], name)
        return cache[key]

    terms = []
    for name in names:
        w = weights.get(name, 0.0)
        if w == 0.0:
            continue
        su, sv = _side(source, src_index, name)
        tu, tv = _side(tgt, tgt_index, name)
        sim, ok = _pooled_similarity(su, sv, tu, tv)
        terms.append((w, sim, ok))
    for key, w in cross_weights.items():
        if w == 0.0:
            continue
        parsed = _cross_sections(key)
        if parsed is None:
            continue
        x, y = parsed
        # 方向性：source 的 x ↔ target 的 y（如 needs_skills）。
        if x not in src_index or y not in tgt_index:
            continue
        su, sv = _side(source, src_index, x)
        tu, tv = _side(tgt, tgt_index, y)
        sim, ok = _pooled_similarity(su, sv, tu, tv)
        terms.append((w, sim, ok))

    m, n = len(source.user_ids), len(tgt.user_ids)
    if not terms:
        dir_matrix = np.zeros((m, n), dtype=float)
    else:
        numer = np.zeros((m, n), dtype=float)
        denom = np.zeros((m, n), dtype=float)
        for w, sim, ok in terms:
            numer += w * np.where(ok, sim, 0.0)
            denom += w * ok.astype(float)
        safe = np.abs(denom) > _EPS
        dir_matrix = np.where(safe, numer / np.where(safe, denom, 1.0), 0.0)

    if square:
        fused_matrix = (dir_matrix + dir_matrix.T) / 2.0
    else:
        fused_matrix = np.array(dir_matrix, copy=True)

    return SimilarityResult(
        source_ids=list(source.user_ids),
        target_ids=list(tgt.user_ids),
        dir_matrix=dir_matrix,
        fused_matrix=fused_matrix,
    )
