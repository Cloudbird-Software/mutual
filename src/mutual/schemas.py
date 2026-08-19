"""Mutual — IO 契约（dataclass schemas）。

这是 spec 的可执行表达：每个 dataclass 对应 spec/01-schemas.md 中的一个契约。
实现代码可以随时重写，但这些 dataclass 的字段和语义不可随意修改。

选 dataclass 而非 pydantic：依赖轻，外部 caller 不需要运行时验证。
如果未来需要运行时验证，加 pydantic adapter，不改 dataclass 本身。
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional

import numpy as np

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------


def hash_text(text: str) -> str:
    """内容 hash，用于 embedding/LLM 缓存的 content-addressed key。

    禁止使用 Python 内置 hash()——它是进程间 salt 的，缓存无法跨 run 命中。
    """
    return hashlib.sha256(text.encode("utf-8")).hexdigest()[:16]


def stable_pair_id(user1: str, user2: str) -> str:
    """稳定的 pair_id：与参数顺序无关。"""
    a, b = sorted([user1, user2])
    return f"{a}__{b}"


# ---------------------------------------------------------------------------
# 1. Profile
# ---------------------------------------------------------------------------


@dataclass
class Profile:
    """用户/实体的原始自由文本画像。"""

    id: str
    sections: Dict[str, str]
    last_updated_at: Optional[str] = None

    def to_dict(self) -> Dict:
        return {
            "id": self.id,
            "sections": self.sections,
            "last_updated_at": self.last_updated_at,
        }

    @classmethod
    def from_dict(cls, d: Dict) -> "Profile":
        return cls(
            id=d["id"],
            sections=d.get("sections", {}),
            last_updated_at=d.get("last_updated_at"),
        )


# ---------------------------------------------------------------------------
# 2. ExtractedSections
# ---------------------------------------------------------------------------


@dataclass
class ExtractedSections:
    """LLM 提取后的结构化分节。"""

    id: str
    sections: Dict[str, str]
    hash: str = ""

    def __post_init__(self):
        if not self.hash:
            self.hash = hash_text(json.dumps(self.sections, sort_keys=True))

    def to_dict(self) -> Dict:
        return {"id": self.id, "sections": self.sections, "hash": self.hash}

    @classmethod
    def from_dict(cls, d: Dict) -> "ExtractedSections":
        return cls(id=d["id"], sections=d["sections"], hash=d.get("hash", ""))


# ---------------------------------------------------------------------------
# 3. HydeDescriptors
# ---------------------------------------------------------------------------


@dataclass
class HydeDescriptors:
    """Hypothetical Document Embeddings：为每个 section 生成假设性描述。"""

    id: str
    descriptors: Dict[str, List[str]]  # {section: [desc1, ...]}

    def to_dict(self) -> Dict:
        return {"id": self.id, "descriptors": self.descriptors}

    @classmethod
    def from_dict(cls, d: Dict) -> "HydeDescriptors":
        return cls(id=d["id"], descriptors=d["descriptors"])


# ---------------------------------------------------------------------------
# 4. EmbeddingsBundle
# ---------------------------------------------------------------------------


@dataclass
class EmbeddingsBundle:
    """所有用户的 embedding 打包，content-hash 驱动增量复用。"""

    user_ids: List[str]
    section_names: List[str]
    embeddings: np.ndarray  # [N, sections, D]
    hyde: Dict[str, np.ndarray]  # {section: [N, n_desc, D]}
    embedding_model: str
    dim: int
    section_hashes: Dict[str, str] = field(default_factory=dict)  # {user_id|section: hash}
    hyde_hashes: Dict[str, str] = field(default_factory=dict)
    user_timestamps: Dict[str, str] = field(default_factory=dict)

    def subset(self, user_ids: List[str]) -> "EmbeddingsBundle":
        """取子集——query(1×M) 和 batch(M×N) 模式的基础原语。"""
        idx = [self.user_ids.index(uid) for uid in user_ids]
        return EmbeddingsBundle(
            user_ids=[self.user_ids[i] for i in idx],
            section_names=self.section_names,
            embeddings=self.embeddings[idx],
            hyde={k: v[idx] for k, v in self.hyde.items()},
            embedding_model=self.embedding_model,
            dim=self.dim,
            section_hashes=self.section_hashes,
            hyde_hashes=self.hyde_hashes,
            user_timestamps=self.user_timestamps,
        )

    def to_dict(self) -> Dict:
        return {
            "user_ids": self.user_ids,
            "section_names": self.section_names,
            "embedding_model": self.embedding_model,
            "dim": self.dim,
            "section_hashes": self.section_hashes,
            "hyde_hashes": self.hyde_hashes,
            "user_timestamps": self.user_timestamps,
        }


# ---------------------------------------------------------------------------
# 5. SimilarityResult
# ---------------------------------------------------------------------------


@dataclass
class SimilarityResult:
    """召回层输出：方向性相似度矩阵。"""

    source_ids: List[str]
    target_ids: List[str]
    dir_matrix: np.ndarray  # [M, N] 方向性（source→target）
    fused_matrix: np.ndarray  # [M, N] 跨 section 融合

    @property
    def is_square(self) -> bool:
        return self.source_ids is self.target_ids or (
            len(self.source_ids) == len(self.target_ids)
            and all(a == b for a, b in zip(self.source_ids, self.target_ids, strict=True))
        )


# ---------------------------------------------------------------------------
# 6. CandidatePair
# ---------------------------------------------------------------------------


@dataclass
class CandidatePair:
    """进入 LLM 精排的候选对。"""

    user1: str
    user2: str
    pair_id: str
    similarity_score: float

    @classmethod
    def create(cls, user1: str, user2: str, score: float) -> "CandidatePair":
        return cls(
            user1=min(user1, user2),
            user2=max(user1, user2),
            pair_id=stable_pair_id(user1, user2),
            similarity_score=score,
        )


# ---------------------------------------------------------------------------
# 7. PairScore
# ---------------------------------------------------------------------------


@dataclass
class PairScore:
    """LLM 精排后的双向打分结果。"""

    pair_id: str
    user1: str
    user2: str
    embed_score: float
    llm_score: Optional[float] = None
    llm_score_a_to_b: Optional[float] = None
    llm_score_b_to_a: Optional[float] = None
    embed_score_normalized: Optional[float] = None
    llm_score_normalized: Optional[float] = None

    def to_dict(self) -> Dict:
        d = {
            "pair_id": self.pair_id,
            "user1": self.user1,
            "user2": self.user2,
            "embed_score": round(self.embed_score, 3),
            "llm_score": round(self.llm_score, 3) if self.llm_score is not None else None,
            "llm_score_a_to_b": round(self.llm_score_a_to_b, 3)
            if self.llm_score_a_to_b is not None
            else None,
            "llm_score_b_to_a": round(self.llm_score_b_to_a, 3)
            if self.llm_score_b_to_a is not None
            else None,
        }
        if self.embed_score_normalized is not None:
            d["embed_score_normalized"] = round(self.embed_score_normalized, 3)
        if self.llm_score_normalized is not None:
            d["llm_score_normalized"] = round(self.llm_score_normalized, 3)
        return d


# ---------------------------------------------------------------------------
# 8. PrefMatrix（互惠核心）
# ---------------------------------------------------------------------------


@dataclass
class PrefMatrix:
    """双向偏好矩阵，作为匹配市场的输入。

    来源：PairScore 的方向性 LLM 分数。
    消费方：FairRec nsw_maximize / sw_maximize。
    """

    left_ids: List[str]
    right_ids: List[str]
    pref_left_to_right: np.ndarray  # [M, N]
    pref_right_to_left: np.ndarray  # [N, M]

    def to_dict(self) -> Dict:
        return {
            "left_ids": self.left_ids,
            "right_ids": self.right_ids,
            "pref_left_to_right": self.pref_left_to_right.tolist(),
            "pref_right_to_left": self.pref_right_to_left.tolist(),
        }


# ---------------------------------------------------------------------------
# 9. Edge
# ---------------------------------------------------------------------------


@dataclass
class Edge:
    """最终匹配边。"""

    user1: str
    user2: str
    pair_id: str
    final_weight: float
    embed_score: float
    llm_score: float
    embed_score_normalized: Optional[float] = None
    llm_score_normalized: Optional[float] = None
    llm_score_a_to_b: Optional[float] = None
    llm_score_b_to_a: Optional[float] = None
    intro: str = ""
    starter_topics: str = ""

    def to_dict(self) -> Dict:
        return {
            "user1": self.user1,
            "user2": self.user2,
            "pair_id": self.pair_id,
            "final_weight": round(self.final_weight, 3),
            "embed_score": round(self.embed_score, 3),
            "llm_score": round(self.llm_score, 3),
            "embed_score_normalized": round(self.embed_score_normalized, 3)
            if self.embed_score_normalized is not None
            else None,
            "llm_score_normalized": round(self.llm_score_normalized, 3)
            if self.llm_score_normalized is not None
            else None,
            "llm_score_a_to_b": round(self.llm_score_a_to_b, 3)
            if self.llm_score_a_to_b is not None
            else None,
            "llm_score_b_to_a": round(self.llm_score_b_to_a, 3)
            if self.llm_score_b_to_a is not None
            else None,
            "intro": self.intro,
            "starter_topics": self.starter_topics,
        }


# ---------------------------------------------------------------------------
# 10. Introduction
# ---------------------------------------------------------------------------


@dataclass
class Introduction:
    """双向对接话术 + 破冰话题。"""

    pair_id: str
    intro: str
    starter_topics: str


# ---------------------------------------------------------------------------
# 11. MatchResult
# ---------------------------------------------------------------------------


@dataclass
class MatchResult:
    """一次匹配运行的完整输出。"""

    edges: List[Edge]
    report_data: Dict[str, Any]
    new_pairs: List[Dict[str, Any]]
    envy_report: Optional[Dict[str, Any]] = None

    def to_dict(self) -> Dict:
        return {
            "edges": [e.to_dict() for e in self.edges],
            "report_data": self.report_data,
            "new_pairs": list(self.new_pairs),
            "envy_report": self.envy_report,
        }


# ---------------------------------------------------------------------------
# 12. EvaluationReport
# ---------------------------------------------------------------------------


@dataclass
class EvaluationReport:
    """评测结果，作为 LLM 自我改进的反馈。"""

    hr_at_1: float
    hr_at_3: float
    hr_at_5: float
    ndcg_at_5: float
    envy_count_left: int = 0
    envy_count_right: int = 0
    total_scenarios: int = 0
    metadata: Dict[str, Any] = field(default_factory=dict)

    @property
    def total_envy(self) -> int:
        return self.envy_count_left + self.envy_count_right

    def passes_gates(self, gates: Dict[str, float]) -> bool:
        """检查是否通过 CI 门禁。"""
        return (
            self.hr_at_3 >= gates.get("hr_at_3_min", 0.6)
            and self.ndcg_at_5 >= gates.get("ndcg_at_5_min", 0.4)
            and self.total_envy <= gates.get("total_envy_max", 2)
        )

    def to_dict(self) -> Dict:
        return {
            "hr_at_1": round(self.hr_at_1, 4),
            "hr_at_3": round(self.hr_at_3, 4),
            "hr_at_5": round(self.hr_at_5, 4),
            "ndcg_at_5": round(self.ndcg_at_5, 4),
            "envy_count_left": self.envy_count_left,
            "envy_count_right": self.envy_count_right,
            "total_scenarios": self.total_scenarios,
            "metadata": self.metadata,
        }
