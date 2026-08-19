"""Mutual — 确定性代理信号源（离线评测用 LLM/embedder 替身）。

Phase 3 评测闭环（docs/engineering-plan.md §5.1）在 CI 中必须离线可复现，
但真实 LLM 打分不可复现且需凭据。本模块提供**语义可解释的确定性替代**：

- :func:`directional_score`：needs↔skills / project↔skills / vision 重叠
  （token 余弦式），模拟 LLM 双向打分的语义判断；
- :func:`embed_score`：TF 向量余弦，模拟 embedding 相似度（冷启动场景的
  唯一信号——无打分历史时系统只能依赖 embedding）；
- 噪声项 ``noise_scale``：固定 seed 的加性噪声，模拟 LLM 判断的不完美
  （无噪声时打分完全等同于 ground truth 构造规则 → HR 恒 1.0，门禁失去
  判别力；有噪声时求解器/排序的退化会真实传导到 HR/NDCG）。

与 fake_llm（spec/04-fixtures.md §7）的区别：fake_llm 按 prompt 查表返回
固定响应（守护 stage 契约）；surrogate 从画像文本计算信号（评测语义）。
"""

from __future__ import annotations

import re
from typing import Dict, List, Sequence

import numpy as np

# 与 config/default.yaml recipe.section_weights 对齐的代理权重
_W_NEEDS_SKILLS = 0.6
_W_PROJECT_SKILLS = 0.2
_W_VISION = 0.2

_WORD_RE = re.compile(r"[a-z0-9]+")

_SECTION_KEYS = ("needs", "project", "skills", "vision")


def tokenize(text: str) -> List[str]:
    """小写化并按非字母数字切词（画像文本约定为英文关键词风格）。"""
    return _WORD_RE.findall(text.lower())


def _tokens_of(sections: Dict[str, str], key: str) -> set[str]:
    return set(tokenize(sections.get(key) or ""))


def _overlap(a: set[str], b: set[str]) -> float:
    """集合重叠的余弦式度量 ∈ [0, 1]；双方皆空记 0（中性偏保守）。"""
    if not a or not b:
        return 0.0
    return len(a & b) / np.sqrt(len(a) * len(b))


def directional_score(
    sections_a: Dict[str, str],
    sections_b: Dict[str, str],
) -> float:
    """A 对 B 的方向性价值分 ∈ [0, 1]（模拟 LLM 的 a_to_b 打分）。

    语义规则（与 recipe.instruction 对齐：A 的需求被 B 的技能满足 +
    共享方向）：
    - 0.6 * overlap(A.needs, B.skills)   —— 需求被技能直击（主信号）
    - 0.2 * overlap(A.project, B.skills) —— 项目协作空间
    - 0.2 * overlap(A.vision, B.vision)  —— 方向一致
    """
    return float(
        _W_NEEDS_SKILLS
        * _overlap(_tokens_of(sections_a, "needs"), _tokens_of(sections_b, "skills"))
        + _W_PROJECT_SKILLS
        * _overlap(_tokens_of(sections_a, "project"), _tokens_of(sections_b, "skills"))
        + _W_VISION * _overlap(_tokens_of(sections_a, "vision"), _tokens_of(sections_b, "vision"))
    )


def tf_vector(texts: Sequence[str]) -> np.ndarray:
    """构建固定词表 TF 向量矩阵（确定性，无外部模型）。"""
    vocab: Dict[str, int] = {}
    token_lists = [tokenize(t) for t in texts]
    for toks in token_lists:
        for tok in toks:
            if tok not in vocab:
                vocab[tok] = len(vocab)
    mat = np.zeros((len(texts), len(vocab)), dtype=float)
    for r, toks in enumerate(token_lists):
        for tok in toks:
            mat[r, vocab[tok]] += 1.0
    # L2 归一化 → 点积即余弦
    norms = np.linalg.norm(mat, axis=1, keepdims=True)
    norms[norms == 0] = 1.0
    return mat / norms


def embed_score(
    sections_a: Dict[str, str],
    sections_b: Dict[str, str],
) -> float:
    """画像级 embedding 相似度 ∈ [0, 1]（四个 section 拼接后 TF 余弦）。"""
    mat = tf_vector([_join_sections(sections_a), _join_sections(sections_b)])
    return float(np.clip(np.dot(mat[0], mat[1]), 0.0, 1.0))


def _join_sections(sections: Dict[str, str]) -> str:
    return " ".join((sections.get(k) or "") for k in _SECTION_KEYS)


def noisy(score: float, rng: np.random.RandomState, noise_scale: float) -> float:
    """加性噪声并截断到 [0, 1]（固定 seed → 确定性）。"""
    return float(np.clip(score + noise_scale * (rng.rand() - 0.5), 0.0, 1.0))


def score_matrix(
    member_sections: Dict[str, Dict[str, str]],
    pool_sections: Dict[str, Dict[str, str]],
    seed: int,
    noise_scale: float = 0.15,
    embedding_only: bool = False,
) -> Dict[str, Dict[str, tuple[float, float]]]:
    """批量计算双向偏好分（模拟 score 阶段输出，喂给 pre_matrix）。

    Args:
        member_sections: ``{member_id → {section → text}}``。
        pool_sections: ``{pool_id → {section → text}}``。
        seed: 噪声随机种子（确定性）。
        noise_scale: 噪声幅度；0 = 完美信号（调试用）。
        embedding_only: True 时退化为纯 embedding 相似度（冷启动：无打分
            历史，双向同值）。

    Returns:
        ``{member_id → {pool_id → (a_to_b, b_to_a)}}``。
    """
    rng = np.random.RandomState(seed)
    out: Dict[str, Dict[str, tuple[float, float]]] = {}
    for mid, msec in member_sections.items():
        row: Dict[str, tuple[float, float]] = {}
        for pid, psec in pool_sections.items():
            if embedding_only:
                s = embed_score(msec, psec)
                row[pid] = (noisy(s, rng, noise_scale), noisy(s, rng, noise_scale))
            else:
                a2b = directional_score(msec, psec)
                b2a = directional_score(psec, msec)
                row[pid] = (noisy(a2b, rng, noise_scale), noisy(b2a, rng, noise_scale))
        out[mid] = row
    return out
