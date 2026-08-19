"""Mutual — Store Protocol + FileStore。

对应 docs/engineering-plan.md §3.2、spec/02-stages.md（adapter 边界）。

Store 是 adapter 层：core 阶段（extract/hyde/embed/score/...）是纯变换，
**不调 Store**；只有 runners 调 Store 做 IO（CLAUDE.md §2.3 铁律）。

FileStore 实现基于文件系统的存储：
- 目录结构 ``{root}/{raw, processed, embeds, outputs, cache}``。
- ``match_history.jsonl`` 为 append-only，每行
  ``{pair_id, user1, user2, matched_at}``，用于 novelty 排除。

边界（spec/05-boundaries.md）：
- §4 失败抽取不持久化（adapter 不得写入 failed_out 报告的项）。
- §6 embedding 复用是 content-addressed（``section_hashes``），不是 roster-addressed。
- §8 ``novelty_window_months`` 过滤 match_history，构建 ``excluded_pairs``。
"""

from __future__ import annotations

import calendar
import json
import re
from abc import ABC, abstractmethod
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional

import numpy as np

from .schemas import Edge, EmbeddingsBundle, ExtractedSections

_BUNDLE_FILENAME = "bundle.npz"
_SECTIONS_SUBDIR = "sections"
_FAILED_MARKER = "Not specified"

# 安全 ID 白名单（qodo #1 路径穿越修复）：profile/section ID 直接拼进文件路径，
# 必须限制为"单段、无分隔符、非点开头"的标识符，杜绝 ``../`` / 绝对路径逃逸。
_SAFE_ID_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]*$")


def _safe_filename(user_id: str) -> Optional[str]:
    """校验 ID 可安全用作文件名；不安全返回 ``None``。

    规则：非空、仅限字母数字与 ``._-``、不得以点开头（``..`` / 隐藏文件）、
    不得含路径分隔符（正则已排除 ``/`` 与 ``\\``）。
    """
    if not isinstance(user_id, str) or not _SAFE_ID_RE.match(user_id):
        return None
    if ".." in user_id:  # 双保险：正则虽允许 ``a..b``，语义上排除连续点
        return None
    return user_id


class Store(ABC):
    """存储抽象协议。

    core 阶段不依赖任何具体 Store 实现；runners 通过此协议做 IO。
    实现可为 FileStore、DBStore、MemoryStore 等。
    """

    @abstractmethod
    def get_sections(self, user_ids: Optional[List[str]] = None) -> Dict[str, ExtractedSections]:
        """读取已提取的 sections。

        Args:
            user_ids: 可选过滤；``None`` 表示返回全部。

        Returns:
            ``{user_id → ExtractedSections}``。
        """
        ...

    @abstractmethod
    def put_sections(self, extracted: List[ExtractedSections]) -> None:
        """持久化提取后的 sections（失败项不应写入，spec/05-boundaries.md §4）。"""
        ...

    @abstractmethod
    def get_embeddings(self) -> Optional[EmbeddingsBundle]:
        """读取已有 EmbeddingsBundle；无则返回 ``None``。

        不同 ``embedding_model`` 的 bundle 应被整体忽略（spec/05-boundaries.md §6）。
        """
        ...

    @abstractmethod
    def put_embeddings(self, bundle: EmbeddingsBundle) -> None:
        """持久化 EmbeddingsBundle（全尺寸存储；MRL 截断在计算时做）。"""
        ...

    @abstractmethod
    def get_match_history(self) -> List[Dict[str, Any]]:
        """读取匹配历史，按 ``novelty_window_months`` 过滤。

        Returns:
            ``[{pair_id, user1, user2, matched_at}, ...]``，仅含窗口内的记录。
        """
        ...

    @abstractmethod
    def put_matches(self, edges: List[Edge]) -> None:
        """将本次新匹配边 append 到 ``match_history.jsonl``。"""
        ...


class FileStore(Store):
    """基于文件系统的 Store 实现。

    目录结构::

        {root}/
        ├── raw/         # 原始 Profile
        ├── processed/   # ExtractedSections / HydeDescriptors
        ├── embeds/      # EmbeddingsBundle
        ├── outputs/     # 匹配结果 / 报告
        └── cache/       # LLM 响应缓存（content-addressed）

    ``match_history.jsonl`` 位于 ``{root}/``，append-only。
    所有可调参数（如 ``novelty_window_months``）由 caller 从 config 注入，不硬编码。
    """

    def __init__(self, root: str, novelty_window_months: int = 6) -> None:
        self.root: Path = Path(root)
        self.novelty_window_months: int = novelty_window_months
        self.raw_dir: Path = self.root / "raw"
        self.processed_dir: Path = self.root / "processed"
        self.embeds_dir: Path = self.root / "embeds"
        self.outputs_dir: Path = self.root / "outputs"
        self.cache_dir: Path = self.root / "cache"
        self.history_path: Path = self.root / "match_history.jsonl"
        # TODO(S5): spec 未写目录创建时机；__init__ 即建齐五个子目录，
        # 使目录结构在任何 IO 之前就可断言（骨架 docstring 的结构即契约）。
        for d in (
            self.raw_dir,
            self.processed_dir,
            self.embeds_dir,
            self.outputs_dir,
            self.cache_dir,
        ):
            d.mkdir(parents=True, exist_ok=True)

    # ------------------------------------------------------------------
    # sections
    # ------------------------------------------------------------------

    def get_sections(self, user_ids: Optional[List[str]] = None) -> Dict[str, ExtractedSections]:
        sections_dir = self.processed_dir / _SECTIONS_SUBDIR
        if not sections_dir.is_dir():
            return {}
        # TODO(S12): 请求的 user_ids 无对应文件时跳过（返回 dict 不含该 key），
        # adapter 语义是"读到什么给什么"，缺失 id 不是错误。
        if user_ids is None:
            files = sorted(sections_dir.glob("*.json"))
        else:
            # 路径穿越守卫（qodo #1）：不安全 ID 跳过，绝不拼入路径。
            files = [
                sections_dir / f"{uid}.json"
                for uid in user_ids
                if _safe_filename(uid) is not None
            ]
        result: Dict[str, ExtractedSections] = {}
        for path in files:
            if not path.exists():
                continue
            with open(path, "r", encoding="utf-8") as f:
                extracted = ExtractedSections.from_dict(json.load(f))
            result[extracted.id] = extracted
        return result

    def put_sections(self, extracted: List[ExtractedSections]) -> None:
        # TODO(S7): §4 说"失败项不持久化"但未给 store 层的机械判定；
        # extract 失败的标志是全部 section 均为 "Not specified"（§4 上文），
        # 此类条目跳过；部分成功的条目（任一 section 有真实内容）照常写入。
        sections_dir = self.processed_dir / _SECTIONS_SUBDIR
        sections_dir.mkdir(parents=True, exist_ok=True)
        for item in extracted:
            if _is_failed_extraction(item):
                continue
            # 路径穿越守卫（qodo #1）：写侧 fail-loud——不安全 ID 直接拒绝，
            # 不允许静默改写 sections 目录之外的文件。
            if _safe_filename(item.id) is None:
                raise ValueError(
                    f"拒绝持久化不安全的 profile id {item.id!r}："
                    "ID 只允许字母数字与 ._- 且不得以点开头（路径穿越守卫）"
                )
            path = sections_dir / f"{item.id}.json"
            with open(path, "w", encoding="utf-8") as f:
                json.dump(item.to_dict(), f, ensure_ascii=False)

    # ------------------------------------------------------------------
    # embeddings
    # ------------------------------------------------------------------

    def get_embeddings(self) -> Optional[EmbeddingsBundle]:
        path = self.embeds_dir / _BUNDLE_FILENAME
        if not path.exists():
            return None
        # TODO(S8): get_embeddings 无参数，store 层无从校验 embedding_model
        # 与当前 config 是否一致（§6 的"整体忽略"）；此处忠实存取，
        # 模型一致性守卫由 embed 阶段（embed_sections 的 existing 检查）执行。
        with np.load(path, allow_pickle=False) as data:
            meta = json.loads(str(data["meta"]))
            hyde = {
                name.split("::", 1)[1]: data[name]
                for name in data.files
                if name.startswith("hyde::")
            }
            return EmbeddingsBundle(
                user_ids=meta["user_ids"],
                section_names=meta["section_names"],
                embeddings=data["embeddings"],
                hyde=hyde,
                embedding_model=meta["embedding_model"],
                dim=meta["dim"],
                section_hashes=meta.get("section_hashes", {}),
                hyde_hashes=meta.get("hyde_hashes", {}),
                user_timestamps=meta.get("user_timestamps", {}),
            )

    def put_embeddings(self, bundle: EmbeddingsBundle) -> None:
        self.embeds_dir.mkdir(parents=True, exist_ok=True)
        arrays: Dict[str, Any] = {"embeddings": bundle.embeddings}
        for section, arr in bundle.hyde.items():
            arrays[f"hyde::{section}"] = arr
        meta = json.dumps(bundle.to_dict(), ensure_ascii=False)
        np.savez(self.embeds_dir / _BUNDLE_FILENAME, meta=np.array(meta), **arrays)

    # ------------------------------------------------------------------
    # match history（novelty 排除的数据源，spec/05-boundaries.md §8）
    # ------------------------------------------------------------------

    def get_match_history(self) -> List[Dict[str, Any]]:
        if not self.history_path.exists():
            return []
        now = datetime.now(timezone.utc)
        cutoff = _months_before(now, self.novelty_window_months)
        records: List[Dict[str, Any]] = []
        for line in self.history_path.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                record = json.loads(line)
            except json.JSONDecodeError:
                continue
            matched_at = _parse_timestamp(record.get("matched_at"))
            # TODO(S10): matched_at 缺失/不可解析的记录保守保留——novelty
            # 排除是安全侧特性，宁可多排除也不放松窗口；坏 JSON 行无法
            # 提取 pair_id，只能跳过。
            if matched_at is None or matched_at >= cutoff:
                records.append(record)
        return records

    def put_matches(self, edges: List[Edge]) -> None:
        self.root.mkdir(parents=True, exist_ok=True)
        # TODO(S9): spec 未定义 matched_at 的格式；写 ISO 8601 UTC
        # （datetime.isoformat 带 +00:00），读取端兼容 "Z"/naive（naive 视为 UTC）。
        matched_at = datetime.now(timezone.utc).isoformat()
        with open(self.history_path, "a", encoding="utf-8") as f:
            for edge in edges:
                record = {
                    "pair_id": edge.pair_id,
                    "user1": edge.user1,
                    "user2": edge.user2,
                    "matched_at": matched_at,
                }
                f.write(json.dumps(record, ensure_ascii=False) + "\n")


# ---------------------------------------------------------------------------
# 私有 helper
# ---------------------------------------------------------------------------


def _is_failed_extraction(item: ExtractedSections) -> bool:
    """§4 的 store 层判定：全部 section 均为 "Not specified"（或无 section）。"""
    if not item.sections:
        return True
    return all(value.strip() == _FAILED_MARKER for value in item.sections.values())


def _parse_timestamp(raw: Any) -> Optional[datetime]:
    """解析 ISO 8601 时间戳；naive 视为 UTC，不可解析返回 ``None``。"""
    if not isinstance(raw, str):
        return None
    try:
        ts = datetime.fromisoformat(raw.replace("Z", "+00:00"))
    except ValueError:
        return None
    if ts.tzinfo is None:
        ts = ts.replace(tzinfo=timezone.utc)
    return ts


def _months_before(now: datetime, months: int) -> datetime:
    """精确回退 ``months`` 个月的日历算术（不引入 dateutil 依赖）。

    TODO(S11): 同日不存在时取当月最后一天（如 3月31日 回退 1 个月 → 2月28/29日）。
    """
    month = now.month - months
    year = now.year
    while month <= 0:
        month += 12
        year -= 1
    day = min(now.day, calendar.monthrange(year, month)[1])
    return now.replace(year=year, month=month, day=day)
