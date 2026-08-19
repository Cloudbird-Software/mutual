"""在线 LLM 链路测试（@pytest.mark.llm，默认跳过）。

运行（需 OPENAI_API_KEY，docs/ci-gates.md §2.7——报告不阻断）：
    RUN_LLM_TESTS=1 pytest tests/test_llm_online.py -m llm

无凭据环境自动 skip，不产生误报。
"""

import os

import pytest

from mutual.llm import LLMWrapper

pytestmark = [
    pytest.mark.llm,
    pytest.mark.skipif(
        not os.environ.get("OPENAI_API_KEY"),
        reason="需要 OPENAI_API_KEY（RUN_LLM_TESTS=1 启用）",
    ),
]


def _llm():
    from mutual.config import load_config

    config = load_config()
    models = config["models"]
    return LLMWrapper(
        model=models["pair_llm"],
        embedding_model=models["embedding"],
        embedding_base_url=models.get("embedding_base_url"),
        base_url=models.get("base_url"),
        reasoning_effort=models.get("pair_reasoning_effort", "medium"),
    )


def test_real_llm_scoring_roundtrip():
    """真实打分链路：双向分数可解析且落在 [0, 1]。"""
    import json

    llm = _llm()
    prompt = (
        "Rate the reciprocal match value of these two people 0.0-1.0.\n"
        "A needs: rust blockchain audit; A skills: distributed systems.\n"
        "B skills: rust blockchain performance; B needs: hard problems.\n"
        'Reply JSON: {"a_to_b": <float>, "b_to_a": <float>}'
    )
    text = llm([{"role": "user", "content": prompt}], model=llm.model)
    data = json.loads(text)
    assert 0.0 <= float(data["a_to_b"]) <= 1.0
    assert 0.0 <= float(data["b_to_a"]) <= 1.0


def test_real_embedding_shape():
    """真实 embedding：批量文本返回统一维度向量。"""
    import numpy as np

    llm = _llm()
    embedder = llm.get_embedding_model()
    vecs = np.asarray(embedder.embed(["rust blockchain", "choir arranging"]))
    assert vecs.shape[0] == 2
    assert vecs.shape[1] > 0
