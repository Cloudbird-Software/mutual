"""Mutual — LLM Wrapper。

对应 docs/engineering-plan.md §3.1、spec/02-stages.md（adapter 边界）。

LLM 调用的统一入口：同步调用、asyncio 桥接、content-addressed 缓存。
core 阶段（extract / hyde / score / introduce）通过 LLMWrapper 访问 LLM，
不直接碰 openai SDK——SDK 细节封装在此处，实现可随时重写而契约不变。

边界（spec/05-boundaries.md）：
- §5 缓存 key = hash_text(完整 prompt)，**禁止**使用 Python 内置 hash()
  （进程间 salt 不同，缓存无法跨 run 命中）。
- §9 所有 LLM 调用经 run_coro_blocking 进入 asyncio，兼容宿主事件循环
  （asyncio.run 在宿主运行中的事件循环里会 raise，导致静默降级）。
"""

from __future__ import annotations

import asyncio
import json
import os
import threading
from pathlib import Path
from typing import Any, Dict, List, Optional, Protocol, runtime_checkable

from .schemas import hash_text


@runtime_checkable
class EmbedderProtocol(Protocol):
    """Embedder 鸭子类型契约。

    实现需提供 ``embed(texts)``，返回 ``[N, D]`` 的 numpy 数组。
    fake_embedder（离线测试）与真实 SDK embedder 都满足此协议。
    """

    def embed(self, texts: List[str]) -> Any:
        """对一组文本生成向量，返回 [N, D] ndarray。"""
        ...


class _AsyncOpenAIEmbedder:
    """openai embeddings 的 :class:`EmbedderProtocol` 实现。

    ``embed`` 是同步门面，内部经 :meth:`LLMWrapper.run_coro_blocking`
    进入 asyncio（spec/05-boundaries.md §9）；openai 在调用点延迟导入，
    离线测试环境 import 本模块不会触发 SDK 依赖。
    """

    def __init__(self, model: str, wrapper: "LLMWrapper") -> None:
        self._model = model
        self._wrapper = wrapper

    def embed(self, texts: List[str]) -> Any:
        return self._wrapper.run_coro_blocking(self._acreate(list(texts)))

    async def _acreate(self, texts: List[str]) -> Any:
        import numpy as np
        from openai import AsyncOpenAI

        # embedding 使用独立的 endpoint/凭据（如 Voyage AI），不与 chat 混用。
        kwargs: Dict[str, Any] = {
            "api_key": self._wrapper.embedding_api_key,
            "base_url": self._wrapper.embedding_base_url,
        }
        async with AsyncOpenAI(**kwargs) as client:
            response = await client.embeddings.create(model=self._model, input=texts)
        return np.vstack([np.asarray(item.embedding, dtype=np.float64) for item in response.data])


class LLMWrapper:
    """LLM + Embedding 的统一包装器。

    所有可调参数从 config 读取后由 caller 注入（不在内部硬编码）：
    - ``cache_dir``：缓存目录；``None`` 时禁用缓存。
    - ``reasoning_effort``：推理强度（low/medium/high），来自 ``models.reasoning_effort``。
    - ``max_concurrent_llm_calls``：并发上限，来自 ``concurrency.max_concurrent_llm_calls``。
    - ``embedding_model``：embedding 模型标识，来自 ``models.embedding``。

    缓存按 ``hash_text(序列化后的完整 prompt)`` 做 content-addressed key：
    prompt 中嵌入了 profile 内容，profile 编辑后 prompt 自动变化、缓存自动失效。
    """

    def __init__(
        self,
        cache_dir: Optional[str] = None,
        reasoning_effort: str = "low",
        max_concurrent_llm_calls: int = 16,
        # TODO(S4): 骨架签名未含 embedding_model，但 docstring 要求从
        # config["models"]["embedding"] 读取（spec 沉默：注入方式未写）。
        # 按 reasoning_effort 的同构方式增加构造参数，默认值取 config 默认。
        embedding_model: str = "text-embedding-3-small",
        api_key: Optional[str] = None,
        base_url: Optional[str] = None,
        model: str = "gpt-4o-mini",
        embedding_api_key: Optional[str] = None,
        embedding_base_url: Optional[str] = None,
    ) -> None:
        # 仅持有配置引用，不做任何 IO（目录在首次写入时惰性创建）。
        self.cache_dir: Optional[Path] = Path(cache_dir) if cache_dir else None
        self.reasoning_effort: str = reasoning_effort
        self.max_concurrent_llm_calls: int = max_concurrent_llm_calls
        self.embedding_model: str = embedding_model
        # 凭据与端点：默认读环境变量（OPENAI_API_KEY / OPENAI_BASE_URL /
        # 自定义 key 变量，见 config 注入）；也可在构造时显式传入。
        self.api_key: str = (
            api_key or os.environ.get("OPENAI_API_KEY") or os.environ.get("MUTUAL_API_KEY") or ""
        )
        self.base_url: Optional[str] = base_url or os.environ.get("OPENAI_BASE_URL")
        self.model: str = model
        # embedding 的 endpoint/凭据：默认读独立环境变量（VOYAGE_API_KEY /
        # EMBEDDING_API_KEY / EMBEDDING_BASE_URL），显式传入优先；无独立配置时
        # 回退到 chat 的 key/base_url（兼容 single-provider 场景）。
        self.embedding_api_key: str = (
            embedding_api_key
            or os.environ.get("VOYAGE_API_KEY")
            or os.environ.get("EMBEDDING_API_KEY")
            or self.api_key
        )
        self.embedding_base_url: Optional[str] = (
            embedding_base_url
            or os.environ.get("VOYAGE_BASE_URL")
            or os.environ.get("EMBEDDING_BASE_URL")
            or self.base_url
        )
        # 本次运行新写入的缓存条目计数（缓存逻辑在 __call__ 中实现）。
        self._cache_writes: int = 0

    @property
    def cache_writes(self) -> int:
        """本次运行新写入的缓存条目数（缓存命中不计入）。"""
        return self._cache_writes

    def __call__(
        self,
        messages: List[Dict[str, str]],
        model: str,
        temperature: float = 0.0,
        max_tokens: Optional[int] = None,
    ) -> str:
        """同步调用 LLM，返回 response text。

        缓存 key = ``hash_text(完整 prompt)``（spec/05-boundaries.md §5）。
        ``cache_dir is None`` 时禁用缓存，每次都真实调用。

        Args:
            messages: chat messages 列表（``[{"role": ..., "content": ...}, ...]``）。
            model: 模型标识（来自 ``config["models"]``，如 pair_llm）。
            temperature: 采样温度。
            max_tokens: 最大输出 token 数。

        Returns:
            LLM 响应文本。
        """
        key = self._cache_key(messages, model, temperature, max_tokens)
        cached = self._read_cache(key)
        if cached is not None:
            return cached
        response = self.run_coro_blocking(
            self._acall_openai(messages, model, temperature, max_tokens)
        )
        self._write_cache(key, response)
        return response

    def run_coro_blocking(self, coro: Any) -> Any:
        """从同步代码执行 asyncio coroutine，兼容宿主事件循环。

        若当前线程已有运行中的事件循环（如 Jupyter / 已有 loop 的宿主），
        用 ``run_until_complete`` 而非 ``asyncio.run``，避免 raise 导致
        LLM 阶段静默降级（spec/05-boundaries.md §9）。

        Args:
            coro: 一个 asyncio coroutine（如聚合并发 LLM 调用的 gather）。

        Returns:
            coroutine 的执行结果。
        """
        try:
            asyncio.get_running_loop()
        except RuntimeError:
            # 当前线程无运行中的事件循环：标准路径。
            return asyncio.run(coro)
        # TODO(S2): spec §9 只说了宿主运行中 loop 时"用 run_until_complete
        # 方式"，但同线程内任何 loop 的 run_until_complete 都会 raise
        # ("Cannot run the event loop while another loop is running")。
        # 为既遵守 run_until_complete 方式又不 raise，把 coroutine 交给
        # 独立线程中的新事件循环执行，当前线程 join 等待结果。
        result: Dict[str, Any] = {}

        def _run_in_thread() -> None:
            loop = asyncio.new_event_loop()
            try:
                result["value"] = loop.run_until_complete(coro)
            except BaseException as exc:  # 原样回传给调用方线程重抛
                result["error"] = exc
            finally:
                loop.close()

        worker = threading.Thread(target=_run_in_thread, name="mutual-llm-coro")
        worker.start()
        worker.join()
        if "error" in result:
            raise result["error"]
        return result["value"]

    def get_embedding_model(self) -> EmbedderProtocol:
        """返回 embedder 实例。

        返回对象满足 :class:`EmbedderProtocol`：``embed(texts) -> [N, D] ndarray``。
        embedding model 标识来自 ``config["models"]["embedding"]``，endpoint/凭据
        走独立的 ``embedding_base_url`` / ``embedding_api_key``（默认 Voyage AI，
        见 ``config/models.embedding_base_url`` 与环境变量 ``VOYAGE_API_KEY``）。

        .. note::
            chat（LongCat-2.0）与 embedding（Voyage）分属不同 provider，端点/密钥
            独立配置；二者均已通过在线测试（tests/test_llm_online.py）。
        """
        return _AsyncOpenAIEmbedder(model=self.embedding_model, wrapper=self)

    # ------------------------------------------------------------------
    # 内部：真实 API 调用（openai 延迟导入）与 content-addressed 缓存
    # ------------------------------------------------------------------

    async def _acall_openai(
        self,
        messages: List[Dict[str, str]],
        model: str,
        temperature: float,
        max_tokens: Optional[int],
    ) -> str:
        """真实 API 调用；openai 在函数体内延迟导入（离线环境可 import 本模块）。"""
        from openai import AsyncOpenAI

        kwargs: Dict[str, Any] = {
            "model": model,
            "messages": messages,
            "temperature": temperature,
        }
        if max_tokens is not None:
            kwargs["max_tokens"] = max_tokens
        if self.reasoning_effort:
            # TODO(S3): spec 未写 reasoning_effort 如何映射到 SDK 参数；
            # 非空时直接透传给 chat.completions.create（不识别模型族）。
            kwargs["reasoning_effort"] = self.reasoning_effort
        client_kwargs: Dict[str, Any] = {}
        if self.api_key:
            client_kwargs["api_key"] = self.api_key
        if self.base_url:
            client_kwargs["base_url"] = self.base_url
        async with AsyncOpenAI(**client_kwargs) as client:
            response = await client.chat.completions.create(**kwargs)
        return response.choices[0].message.content or ""

    def _cache_key(
        self,
        messages: List[Dict[str, str]],
        model: str,
        temperature: float,
        max_tokens: Optional[int],
    ) -> str:
        """序列化完整调用参数并取 ``hash_text`` 作为 content-addressed key。"""
        # TODO(S1): spec §5 只说"完整 prompt 的 hash"，未说明是否纳入
        # 采样参数与模型标识。此处将 model/temperature/max_tokens 一并
        # 序列化——换模型或温度必须使缓存失效，否则返回 stale 响应
        # （与 §5 对 roster-keyed 缓存的批评同一立场上）。
        payload = json.dumps(
            {
                "messages": messages,
                "model": model,
                "temperature": temperature,
                "max_tokens": max_tokens,
            },
            sort_keys=True,
            ensure_ascii=False,
            separators=(",", ":"),
        )
        return hash_text(payload)

    def _read_cache(self, key: str) -> Optional[str]:
        """命中则返回缓存响应文本；未命中/禁用缓存返回 ``None``。"""
        if self.cache_dir is None:
            return None
        path = self.cache_dir / f"{key}.json"
        if not path.exists():
            return None
        with open(path, "r", encoding="utf-8") as f:
            payload = json.load(f)
        return payload["response"]

    def _write_cache(self, key: str, response: str) -> None:
        """写入缓存文件（目录惰性创建）并累加 :attr:`cache_writes`。"""
        if self.cache_dir is None:
            return
        self.cache_dir.mkdir(parents=True, exist_ok=True)
        path = self.cache_dir / f"{key}.json"
        with open(path, "w", encoding="utf-8") as f:
            json.dump({"key": key, "response": response}, f, ensure_ascii=False)
        self._cache_writes += 1
