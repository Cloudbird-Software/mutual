"""守护 spec/04-fixtures.md §7 的 fake 确定性契约。

黑盒方式：独立复写 spec §7.1 的分数表，验证 conftest 的 fake_llm /
fake_embedder 行为与 spec 逐位一致（防止 conftest 实现漂移）。
"""

import json

import numpy as np

# spec/04-fixtures.md §7.1 打分分数表（pair_id -> (a_to_b, b_to_a)）
SPEC_TABLE = {
    "alice__bob": (0.85, 0.90),
    "alice__carol": (0.80, 0.82),
    "bob__carol": (0.83, 0.82),
    "alice__david": (0.52, 0.63),
    "bob__david": (0.45, 0.58),
    "carol__david": (0.35, 0.65),
}


def _scoring_prompt(a: str, b: str) -> list:
    """打分类 prompt 必含输出格式标记 a_to_b（§7.1 路由规则）。"""
    return [
        {"role": "user", "content": f"Score pair {a} and {b}. Reply JSON with a_to_b and b_to_a."}
    ]


class TestFakeLLM:
    def test_table_lookup_and_asymmetry(self, fake_llm):
        for pair_id, (a_to_b, b_to_a) in SPEC_TABLE.items():
            a, b = pair_id.split("__")
            resp = json.loads(fake_llm(_scoring_prompt(a, b)))
            assert resp["a_to_b"] == a_to_b
            assert resp["b_to_a"] == b_to_a
            assert resp["a_to_b"] != resp["b_to_a"], f"{pair_id} 方向性被对称化"

    def test_stats_consistent_with_cohort_fixture(self, fake_llm, golden_cohort):
        values = [v for pair in SPEC_TABLE.values() for v in pair]
        stats = golden_cohort["score_statistics"]["llm_scores"]
        assert min(values) == stats["min"]
        assert max(values) == stats["max"]
        assert round(sum(values) / len(values), 3) == stats["avg"]

    def test_deterministic_repeated_calls(self, fake_llm):
        p = _scoring_prompt("alice", "bob")
        assert fake_llm(p) == fake_llm(p)

    def test_default_fallback_symmetric(self, fake_llm):
        resp = json.loads(fake_llm(_scoring_prompt("zoe", "yan")))
        assert resp["a_to_b"] == resp["b_to_a"] == 0.5

    def test_non_scoring_prompt_returns_intro_template(self, fake_llm):
        resp = json.loads(fake_llm([{"role": "user", "content": "Introduce alice and bob."}]))
        assert resp == {"intro": "Fake intro.", "starter_topics": "Fake topic."}

    def test_counters(self, fake_llm):
        fake_llm(_scoring_prompt("alice", "bob"))
        fake_llm([{"role": "user", "content": "hi"}])
        assert fake_llm.call_count == 2
        assert fake_llm.cache_writes == 0


class TestFakeEmbedder:
    def test_shape(self, fake_llm):
        out = fake_llm.get_embedding_model().embed(["alpha", "beta"])
        assert out.shape == (2, 128)

    def test_bitwise_deterministic_across_instances(self, fake_llm):
        a1 = fake_llm.get_embedding_model().embed(["alpha", "beta"])
        a2 = fake_llm.get_embedding_model().embed(["alpha", "beta"])
        assert np.array_equal(a1, a2)

    def test_seed_is_hash_text_based(self, fake_llm):
        """种子必须来自 hash_text（sha256），不是内置 hash()（§7.2）。"""
        from mutual.schemas import hash_text

        expected = np.random.RandomState(int(hash_text("alpha"), 16) % 2**32).randn(128)
        actual = fake_llm.get_embedding_model().embed(["alpha"])[0]
        assert np.array_equal(actual, expected)
