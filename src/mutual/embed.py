"""Mutual — Embedding 生成。

对应 docs/engineering-plan.md §3.5、spec/02-stages.md §3。

生成 section + HyDE 向量，content-hash 驱动增量复用。
``embed_sections`` 是纯变换；``load_bundle`` / ``dump_bundle`` 是 adapter 用的磁盘 helper。

边界（spec/05-boundaries.md §6）：
- 复用是 **content-addressed**（``section_hashes``），不是 roster-addressed：
  改一个 profile 只重嵌该 profile 的变化 cell，不影响其他人。
- 不同 ``embedding_model`` 的 ``existing`` bundle 整体忽略（迁移守卫）；
  同名 model 但维度不一致时同样整体忽略（全量重嵌）。
- 全尺寸向量始终存储；MRL 截断在工作副本上做（``truncate_embeddings``）。
- 缓存/复用 key 用 ``hash_text``，禁止内置 ``hash()``。

spec 沉默：
- A-2：embed stage 的 input_schema（spec/02-stages.md §3）未声明 embedder
  注入方式。实现约定：可选 keyword 参数 ``embedder``（鸭子类型，
  ``embed(texts) -> [N, D]``，见 llm.EmbedderProtocol）；为 ``None`` 时尝试
  ``config["llm_wrapper"].get_embedding_model()``。
- A-3：``EmbeddingsBundle.user_timestamps`` 的来源在 embed 输入
  （``ExtractedSections`` 无时间戳字段）中不可得。实现：保留 ``existing``
  中已有用户的时间戳，新用户缺省不记录。
"""

from __future__ import annotations

import json
from typing import Any, Dict, List, Optional, Tuple

import numpy as np

from .extract import NOT_SPECIFIED
from .schemas import EmbeddingsBundle, HydeDescriptors, hash_text

_EPS = 1e-12


def embed_sections(
    sections: list,
    hyde: Dict[str, HydeDescriptors],
    config: Dict[str, Any],
    existing: Optional[EmbeddingsBundle] = None,
    embedder: Optional[Any] = None,
) -> EmbeddingsBundle:
    """生成 section + HyDE 向量；content-hash 驱动增量复用。

    所有可调参数从 ``config`` 读取（``models.embedding``、
    ``models.embedding_dimensions``），不硬编码。

    Args:
        sections: ``list[ExtractedSections]``。
        hyde: ``dict[user_id → HydeDescriptors]``。
        config: 配置 dict。
        existing: 已有 bundle，用于增量复用。``None`` 表示全量重嵌。
            若 ``existing.embedding_model`` 与 config 不一致，整体忽略（§6）。
        embedder: embedder 实例（鸭子类型 ``embed(texts) -> [N, D]``）。
            为 ``None`` 时从 ``config["llm_wrapper"]`` 获取（A-2）。

    Returns:
        :class:`~mutual.schemas.EmbeddingsBundle`，全尺寸存储；
        ``embeddings`` 形状 ``[N, sections, D]``，``hyde`` 形状
        ``{section: [N, n_desc, D]}``。

    边界：复用是 content-addressed 不是 roster-addressed（§6）。
    """
    model = config.get("models", {}).get("embedding", "")
    if embedder is None:
        wrapper = config.get("llm_wrapper")
        getter = getattr(wrapper, "get_embedding_model", None)
        if getter is not None:
            embedder = getter()
    if embedder is None:
        raise ValueError("embedder 未注入：传入 embedder 参数，或在 config 中提供 llm_wrapper")

    user_ids = [es.id for es in sections]
    section_names = sorted({name for es in sections for name in es.sections})
    section_index = {name: k for k, name in enumerate(section_names)}

    reuse = existing is not None and existing.embedding_model == model
    old_user_index: Dict[str, int] = {}
    old_section_index: Dict[str, int] = {}
    if reuse:
        assert existing is not None
        old_user_index = {uid: k for k, uid in enumerate(existing.user_ids)}
        old_section_index = {name: k for k, name in enumerate(existing.section_names)}

    def _plan(reuse_enabled: bool):
        """构建 cell 级嵌入计划。

        Returns:
            base_plan: ``{(user_i, section): ("reuse", old_i, old_si, hash)
            | ("new", text_pos, hash) | None}``；``None`` = 缺失 cell（零向量）。
            hyde_plan: ``{(user_i, section, k): ("reuse", old_i, old_k, hash)
            | ("new", text_pos, hash)}``。
            texts: 待嵌入文本（顺序对应 ``text_pos``）。
        """
        existing_ref = existing if reuse_enabled else None
        base_plan: Dict[Tuple[int, str], Any] = {}
        hyde_plan: Dict[Tuple[int, str, int], Any] = {}
        texts: List[str] = []
        for i, es in enumerate(sections):
            uid = es.id
            for name in section_names:
                content = es.sections.get(name, "")
                present = bool(content) and content != NOT_SPECIFIED
                if not present:
                    base_plan[(i, name)] = None
                    continue
                h = hash_text(content)
                reused = (
                    existing_ref is not None
                    and uid in old_user_index
                    and name in old_section_index
                    and existing_ref.section_hashes.get(f"{uid}|{name}") == h
                )
                if reused:
                    base_plan[(i, name)] = (
                        "reuse",
                        old_user_index[uid],
                        old_section_index[name],
                        h,
                    )
                else:
                    base_plan[(i, name)] = ("new", len(texts), h)
                    texts.append(content)
            hd = hyde.get(uid)
            if hd is None:
                continue
            for name, descs in hd.descriptors.items():
                if name not in section_index:
                    continue
                for k, desc in enumerate(descs):
                    h = hash_text(desc)
                    old_hyde = existing_ref.hyde.get(name) if existing_ref else None
                    reused = (
                        old_hyde is not None
                        and existing_ref is not None
                        and uid in old_user_index
                        and k < old_hyde.shape[1]
                        and existing_ref.hyde_hashes.get(f"{uid}|{name}|{k}") == h
                    )
                    if reused:
                        hyde_plan[(i, name, k)] = ("reuse", old_user_index[uid], k, h)
                    else:
                        hyde_plan[(i, name, k)] = ("new", len(texts), h)
                        texts.append(desc)
        return base_plan, hyde_plan, texts

    base_plan, hyde_plan, texts = _plan(reuse)
    vecs = embedder.embed(texts) if texts else None
    if vecs is not None:
        dim = int(vecs.shape[1])
    elif reuse and existing is not None:
        dim = int(existing.dim)
    else:
        dim = 0

    if reuse and existing is not None and vecs is not None and dim != int(existing.dim):
        # 迁移守卫：同名 model 但维度变化 → existing 整体忽略，全量重嵌。
        reuse = False
        base_plan, hyde_plan, texts = _plan(False)
        vecs = embedder.embed(texts) if texts else None
        dim = int(vecs.shape[1]) if vecs is not None else 0

    n_users = len(user_ids)
    n_sections = len(section_names)

    def _vec(arr: Any, pos: int) -> np.ndarray:
        return np.asarray(arr)[pos]

    embeddings = np.zeros((n_users, n_sections, dim), dtype=float)
    section_hashes: Dict[str, str] = {}
    for (i, name), cell in base_plan.items():
        if cell is None:
            continue
        if cell[0] == "reuse":
            assert existing is not None
            embeddings[i, section_index[name]] = existing.embeddings[cell[1], cell[2]]
            h = cell[3]
        else:
            embeddings[i, section_index[name]] = _vec(vecs, cell[1])
            h = cell[2]
        section_hashes[f"{user_ids[i]}|{name}"] = h

    n_desc: Dict[str, int] = {name: 0 for name in section_names}
    for hd in hyde.values():
        for name, descs in hd.descriptors.items():
            if name in n_desc:
                n_desc[name] = max(n_desc[name], len(descs))
    hyde_arrays: Dict[str, np.ndarray] = {
        name: np.zeros((n_users, count, dim), dtype=float) for name, count in n_desc.items()
    }
    hyde_hashes: Dict[str, str] = {}
    for (i, name, k), cell in hyde_plan.items():
        if cell[0] == "reuse":
            assert existing is not None
            hyde_arrays[name][i, k] = existing.hyde[name][cell[1], cell[2]]
            h = cell[3]
        else:
            hyde_arrays[name][i, k] = _vec(vecs, cell[1])
            h = cell[2]
        hyde_hashes[f"{user_ids[i]}|{name}|{k}"] = h

    user_timestamps: Dict[str, str] = (
        dict(existing.user_timestamps) if existing is not None and reuse else {}
    )

    return EmbeddingsBundle(
        user_ids=user_ids,
        section_names=section_names,
        embeddings=embeddings,
        hyde=hyde_arrays,
        embedding_model=model,
        dim=dim,
        section_hashes=section_hashes,
        hyde_hashes=hyde_hashes,
        user_timestamps=user_timestamps,
    )


def supports_mrl(model: str) -> bool:
    """检查 embedding model 是否支持 Matryoshka Representation Learning。

    支持 MRL 的模型（如 ``text-embedding-3-*``）可安全截断到更低维度；
    不支持的模型截断会破坏语义，应回退到全尺寸。

    spec 沉默 A-12：MRL 模型清单未在 spec/config 中定义。实现按 OpenAI
    ``text-embedding-3-*`` 家族前缀判定。

    Args:
        model: embedding model 标识（来自 ``config["models"]["embedding"]``）。

    Returns:
        是否支持 MRL 截断。
    """
    return model.startswith("text-embedding-3")


def truncate_embeddings(embeddings: np.ndarray, dim: int) -> np.ndarray:
    """MRL 截断：在全尺寸工作副本上截断到 ``dim`` 维。

    全尺寸向量始终存储；截断只发生在计算时的副本上（spec/05-boundaries.md §6）。
    截断后做 L2 归一化（零向量保持为零）。

    Args:
        embeddings: 全尺寸向量，形状 ``[..., D_full]``。
        dim: 目标维度（来自 ``config["models"]["embedding_dimensions"]``）。

    Returns:
        截断后的向量，形状 ``[..., dim]``。
    """
    if dim <= 0:
        raise ValueError(f"dim 必须为正整数，got {dim}")
    truncated = np.array(embeddings[..., :dim], dtype=float, copy=True)
    norms = np.linalg.norm(truncated, axis=-1, keepdims=True)
    return truncated / np.where(norms > _EPS, norms, 1.0)


def load_bundle(path: str) -> EmbeddingsBundle:
    """从磁盘加载 ``EmbeddingsBundle``（adapter 用）。

    Args:
        path: bundle 文件路径（npz + 元数据）。

    Returns:
        :class:`~mutual.schemas.EmbeddingsBundle`。
    """
    with np.load(path, allow_pickle=False) as z:
        meta = json.loads(str(z["meta"][()]))
        embeddings = np.asarray(z["embeddings"])
        hyde = {
            key.split("hyde::", 1)[1]: np.asarray(z[key])
            for key in z.files
            if key.startswith("hyde::")
        }
    return EmbeddingsBundle(
        user_ids=list(meta["user_ids"]),
        section_names=list(meta["section_names"]),
        embeddings=embeddings,
        hyde=hyde,
        embedding_model=meta["embedding_model"],
        dim=int(meta["dim"]),
        section_hashes=meta.get("section_hashes", {}),
        hyde_hashes=meta.get("hyde_hashes", {}),
        user_timestamps=meta.get("user_timestamps", {}),
    )


def dump_bundle(bundle: EmbeddingsBundle, path: str) -> None:
    """写入 ``EmbeddingsBundle`` 到磁盘（adapter 用，全尺寸存储）。

    Args:
        bundle: 待持久化的 bundle。
        path: 目标文件路径。
    """
    meta = json.dumps(
        {
            "user_ids": bundle.user_ids,
            "section_names": bundle.section_names,
            "embedding_model": bundle.embedding_model,
            "dim": int(bundle.dim),
            "section_hashes": bundle.section_hashes,
            "hyde_hashes": bundle.hyde_hashes,
            "user_timestamps": bundle.user_timestamps,
        },
        ensure_ascii=False,
    )
    arrays: Dict[str, Any] = {
        "embeddings": np.asarray(bundle.embeddings),
        "meta": np.array(meta),
    }
    for name, arr in bundle.hyde.items():
        arrays[f"hyde::{name}"] = np.asarray(arr)
    with open(path, "wb") as fh:
        np.savez(fh, **arrays)
