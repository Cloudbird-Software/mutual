"""LLMWrapper 离线单元测试。

不联网、不依赖 openai 安装：真实 API 调用 seam（``_acall_openai``）被
monkeypatch 替换，或注入 fake ``openai`` 模块走延迟导入的完整路径。
缓存行为（命中/失效/计数/禁用）与 asyncio 桥接（无 loop / 有运行中 loop）
按 spec/05-boundaries.md §5、§9 验证。
"""

import asyncio
import json
import sys
import types
from typing import Any, Dict, List

import numpy as np
import pytest

from mutual.llm import EmbedderProtocol, LLMWrapper
from mutual.schemas import hash_text

MESSAGES = [{"role": "user", "content": "Score alice and bob. Reply JSON with a_to_b and b_to_a."}]


@pytest.fixture
def wrapper(config, tmp_path):
    """参数全部从 config 注入（不硬编码），缓存目录指向 tmp_path。"""
    return LLMWrapper(
        cache_dir=str(tmp_path / "llm-cache"),
        reasoning_effort=config["models"]["reasoning_effort"],
        max_concurrent_llm_calls=config["concurrency"]["max_concurrent_llm_calls"],
        embedding_model=config["models"]["embedding"],
    )


@pytest.fixture
def api_calls(monkeypatch):
    """替换真实 API seam：记录调用参数，返回可区分的确定性文本。"""
    calls: List[Dict[str, Any]] = []

    async def fake_acall(self, messages, model, temperature, max_tokens):
        calls.append(
            {
                "messages": messages,
                "model": model,
                "temperature": temperature,
                "max_tokens": max_tokens,
            }
        )
        return f"resp-{len(calls)}"

    monkeypatch.setattr(LLMWrapper, "_acall_openai", fake_acall)
    return calls


def _install_fake_openai(monkeypatch, log: List[Dict[str, Any]]) -> None:
    """向 sys.modules 注入 fake ``openai`` 模块，覆盖延迟导入路径。"""
    module = types.ModuleType("openai")

    class _Completions:
        async def create(self, **kwargs):
            log.append(kwargs)
            message = types.SimpleNamespace(content="api-reply")
            return types.SimpleNamespace(choices=[types.SimpleNamespace(message=message)])

    class _Embeddings:
        async def create(self, **kwargs):
            log.append(kwargs)
            data = [
                types.SimpleNamespace(embedding=[float(len(t)), 1.0, 0.0]) for t in kwargs["input"]
            ]
            return types.SimpleNamespace(data=data)

    class _AsyncOpenAI:
        def __init__(self, api_key=None, base_url=None):
            self.api_key = api_key
            self.base_url = base_url
            self.chat = types.SimpleNamespace(completions=_Completions())
            self.embeddings = _Embeddings()

        async def __aenter__(self):
            return self

        async def __aexit__(self, *exc_info):
            return False

    module.AsyncOpenAI = _AsyncOpenAI
    monkeypatch.setitem(sys.modules, "openai", module)


class TestCache:
    def test_cache_hit_skips_api_call(self, wrapper, api_calls, config):
        model = config["models"]["pair_llm"]
        r1 = wrapper(MESSAGES, model)
        r2 = wrapper(MESSAGES, model)
        assert r1 == r2 == "resp-1"
        assert len(api_calls) == 1
        assert wrapper.cache_writes == 1  # 命中不计入

    def test_cache_invalidated_when_prompt_changes(self, wrapper, api_calls, config):
        model = config["models"]["pair_llm"]
        r1 = wrapper(MESSAGES, model)
        changed = [{"role": "user", "content": MESSAGES[0]["content"] + " (edited profile)"}]
        r2 = wrapper(changed, model)
        assert r1 != r2  # profile 编辑 → prompt 变 → key 变（§5）
        assert len(api_calls) == 2
        assert wrapper.cache_writes == 2

    def test_cache_key_includes_sampling_params(self, wrapper, api_calls, config):
        """S1 假设：model/temperature/max_tokens 纳入 key，换参即失效。"""
        model = config["models"]["pair_llm"]
        wrapper(MESSAGES, model, temperature=0.0)
        wrapper(MESSAGES, model, temperature=0.7)
        assert len(api_calls) == 2
        assert wrapper.cache_writes == 2

    def test_cache_disabled(self, config, api_calls):
        w = LLMWrapper(
            cache_dir=None,
            reasoning_effort=config["models"]["reasoning_effort"],
            max_concurrent_llm_calls=config["concurrency"]["max_concurrent_llm_calls"],
        )
        model = config["models"]["pair_llm"]
        assert w(MESSAGES, model) == "resp-1"
        assert w(MESSAGES, model) == "resp-2"  # 每次都真实调用
        assert w.cache_writes == 0
        assert w.cache_dir is None

    def test_cache_shared_across_instances(self, config, tmp_path, api_calls):
        """content-addressed key 跨 wrapper 实例（等价跨 run）命中。"""
        cache_dir = str(tmp_path / "shared-cache")
        w1 = LLMWrapper(cache_dir=cache_dir)
        w2 = LLMWrapper(cache_dir=cache_dir)
        model = config["models"]["pair_llm"]
        assert w1(MESSAGES, model) == w2(MESSAGES, model)
        assert len(api_calls) == 1
        assert w1.cache_writes == 1
        assert w2.cache_writes == 0

    def test_cache_file_is_hash_text_addressed(self, wrapper, api_calls, config):
        """铁律：缓存文件名 = hash_text(序列化 prompt)，与内置 hash() 无关。"""
        model = config["models"]["pair_llm"]
        wrapper(MESSAGES, model)
        files = list(wrapper.cache_dir.glob("*.json"))
        assert len(files) == 1
        payload = json.dumps(
            {
                "messages": MESSAGES,
                "model": model,
                "temperature": 0.0,
                "max_tokens": None,
            },
            sort_keys=True,
            ensure_ascii=False,
            separators=(",", ":"),
        )
        assert files[0].stem == hash_text(payload)


class TestRunCoroBlocking:
    def test_without_running_loop(self, wrapper):
        async def coro():
            await asyncio.sleep(0)
            return 42

        assert wrapper.run_coro_blocking(coro()) == 42

    def test_with_running_loop(self, wrapper):
        """宿主事件循环运行中（Jupyter 场景）：不得 raise，不得静默降级（§9）。"""

        async def inner():
            await asyncio.sleep(0)
            return "inner-done"

        async def host():
            return wrapper.run_coro_blocking(inner())

        assert asyncio.run(host()) == "inner-done"

    def test_supports_gathered_coros(self, wrapper):
        async def job(i):
            await asyncio.sleep(0)
            return i * 2

        async def gathered():
            return await asyncio.gather(job(1), job(2), job(3))

        assert wrapper.run_coro_blocking(gathered()) == [2, 4, 6]


class TestEmbeddingModel:
    def test_satisfies_embedder_protocol(self, wrapper):
        embedder = wrapper.get_embedding_model()
        assert isinstance(embedder, EmbedderProtocol)
        assert callable(embedder.embed)


class TestRealPathWithFakeOpenAI:
    def test_call_walks_lazy_import_and_cache(self, monkeypatch, wrapper, config):
        log: List[Dict[str, Any]] = []
        _install_fake_openai(monkeypatch, log)
        model = config["models"]["pair_llm"]
        r1 = wrapper(MESSAGES, model)
        r2 = wrapper(MESSAGES, model)
        assert r1 == r2 == "api-reply"
        assert len(log) == 1  # 第二次命中缓存
        assert log[0]["model"] == model
        assert log[0]["reasoning_effort"] == config["models"]["reasoning_effort"]
        assert wrapper.cache_writes == 1

    def test_embedder_walks_lazy_import(self, monkeypatch, wrapper, config):
        log: List[Dict[str, Any]] = []
        _install_fake_openai(monkeypatch, log)
        out = wrapper.get_embedding_model().embed(["a", "bb", "ccc"])
        assert out.shape == (3, 3)  # [N, D]
        assert np.allclose(out[0], [1.0, 1.0, 0.0])
        assert np.allclose(out[2], [3.0, 1.0, 0.0])
        assert log[0]["model"] == config["models"]["embedding"]
        assert log[0]["input"] == ["a", "bb", "ccc"]
