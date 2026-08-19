"""Mutual — pytest 公共 fixture。

fake_llm / fake_embedder 的确定性契约见 spec/04-fixtures.md §7
（契约由 tests/test_fakes.py 守护，变更契约 = spec 变更）。
"""

import json
from pathlib import Path
from typing import Dict, List

import pytest

from mutual.schemas import Profile

GOLDEN_DIR = Path(__file__).parent / "golden"

# fake_llm 打分分数表（spec/04-fixtures.md §7.1，与 cohort.json 统计自洽）。
FAKE_SCORE_TABLE: Dict[str, Dict[str, float]] = {
    "alice__bob": {"a_to_b": 0.85, "b_to_a": 0.90},
    "alice__carol": {"a_to_b": 0.80, "b_to_a": 0.82},
    "bob__carol": {"a_to_b": 0.83, "b_to_a": 0.82},
    "alice__david": {"a_to_b": 0.52, "b_to_a": 0.63},
    "bob__david": {"a_to_b": 0.45, "b_to_a": 0.58},
    "carol__david": {"a_to_b": 0.35, "b_to_a": 0.65},
}

_COHORT_IDS = ("alice", "bob", "carol", "david")
_INTRO_RESPONSE = '{"intro": "Fake intro.", "starter_topics": "Fake topic."}'
_DEFAULT_SCORE_RESPONSE = '{"a_to_b": 0.5, "b_to_a": 0.5, "reasoning": "fake"}'


def _scoring_response(prompt_text: str) -> str:
    """打分类路径：按 prompt 中出现的 cohort id 查表（§7.1）。"""
    found = sorted(uid for uid in _COHORT_IDS if uid in prompt_text)
    if len(found) >= 2:
        entry = FAKE_SCORE_TABLE.get(f"{found[0]}__{found[1]}")
        if entry is not None:
            return json.dumps(
                {"a_to_b": entry["a_to_b"], "b_to_a": entry["b_to_a"], "reasoning": "fake"}
            )
    return _DEFAULT_SCORE_RESPONSE


@pytest.fixture
def golden_basic_dir() -> Path:
    return GOLDEN_DIR / "test_basic"


@pytest.fixture
def golden_profiles(golden_basic_dir) -> List[Profile]:
    """加载 test_basic 下的所有用户 profile。"""
    profiles = []
    for f in sorted(golden_basic_dir.glob("*.json")):
        if f.name == "cohort.json":
            continue
        with open(f, "r", encoding="utf-8") as fh:
            data = json.load(fh)
        profiles.append(Profile.from_dict(data))
    return profiles


@pytest.fixture
def golden_cohort(golden_basic_dir) -> Dict:
    """加载期望的 cohort 匹配结果。"""
    with open(golden_basic_dir / "cohort.json", "r", encoding="utf-8") as f:
        return json.load(f)


@pytest.fixture
def fake_llm():
    """离线 golden test 用的 fake LLM（契约见 spec/04-fixtures.md §7.1）。"""

    class FakeLLM:
        def __init__(self):
            self.cache_writes = 0
            self.call_count = 0

        def __call__(self, messages, **kwargs):
            self.call_count += 1
            prompt_text = " ".join(str(m.get("content", "")) for m in messages)
            if "a_to_b" in prompt_text:
                return _scoring_response(prompt_text)
            return _INTRO_RESPONSE

        def get_embedding_model(self):
            class FakeEmbedder:
                def embed(self, texts):
                    import numpy as np

                    from mutual.schemas import hash_text

                    rows = [
                        np.random.RandomState(int(hash_text(t), 16) % 2**32).randn(128)
                        for t in texts
                    ]
                    return np.vstack(rows)

            return FakeEmbedder()

    return FakeLLM()


@pytest.fixture
def config():
    """加载默认配置。"""
    from mutual.config import load_config

    return load_config()
