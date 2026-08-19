"""Mutual — Schema 契约测试。

验证 dataclass 的 to_dict/from_dict 往返一致性。
这些测试是 spec 的可执行断言：dataclass 字段变化时必须更新此处。
"""

import json

import numpy as np

from mutual.schemas import (
    CandidatePair,
    Edge,
    EmbeddingsBundle,
    EvaluationReport,
    ExtractedSections,
    HydeDescriptors,
    PairScore,
    PrefMatrix,
    Profile,
    hash_text,
    stable_pair_id,
)


class TestHelpers:
    def test_hash_text_deterministic(self):
        assert hash_text("hello") == hash_text("hello")

    def test_hash_text_different(self):
        assert hash_text("hello") != hash_text("world")

    def test_stable_pair_id_order_invariant(self):
        assert stable_pair_id("alice", "bob") == stable_pair_id("bob", "alice")

    def test_stable_pair_id_format(self):
        pid = stable_pair_id("alice", "bob")
        assert pid == "alice__bob"


class TestProfile:
    def test_to_dict_from_dict_roundtrip(self):
        p = Profile(
            id="alice",
            sections={"skills": "art", "needs": "tech help"},
            last_updated_at="2026-08-14T00:00:00Z",
        )
        d = p.to_dict()
        p2 = Profile.from_dict(d)
        assert p2.id == p.id
        assert p2.sections == p.sections
        assert p2.last_updated_at == p.last_updated_at

    def test_from_dict_missing_optional(self):
        p = Profile.from_dict({"id": "bob", "sections": {}})
        assert p.last_updated_at is None


class TestExtractedSections:
    def test_auto_hash(self):
        es = ExtractedSections(id="alice", sections={"skills": "art"})
        assert es.hash != ""
        assert es.hash == hash_text(json.dumps({"skills": "art"}, sort_keys=True))

    def test_hash_stable_for_same_content(self):
        es1 = ExtractedSections(id="a", sections={"skills": "x"})
        es2 = ExtractedSections(id="b", sections={"skills": "x"})
        assert es1.hash == es2.hash  # 内容相同 → hash 相同

    def test_roundtrip(self):
        es = ExtractedSections(id="alice", sections={"skills": "art"}, hash="abc123")
        d = es.to_dict()
        es2 = ExtractedSections.from_dict(d)
        assert es2.id == es.id
        assert es2.sections == es.sections
        assert es2.hash == es.hash


class TestHydeDescriptors:
    def test_roundtrip(self):
        hd = HydeDescriptors(id="alice", descriptors={"skills": ["desc1", "desc2"]})
        d = hd.to_dict()
        hd2 = HydeDescriptors.from_dict(d)
        assert hd2.id == hd.id
        assert hd2.descriptors == hd.descriptors


class TestEmbeddingsBundle:
    def test_subset(self):
        bundle = EmbeddingsBundle(
            user_ids=["alice", "bob", "carol"],
            section_names=["skills", "needs"],
            embeddings=np.random.randn(3, 2, 128),
            hyde={"skills": np.random.randn(3, 1, 128)},
            embedding_model="test-model",
            dim=128,
        )
        sub = bundle.subset(["alice", "carol"])
        assert sub.user_ids == ["alice", "carol"]
        assert sub.embeddings.shape == (2, 2, 128)
        assert sub.embedding_model == "test-model"


class TestCandidatePair:
    def test_create_order_invariant(self):
        cp1 = CandidatePair.create("alice", "bob", 0.8)
        cp2 = CandidatePair.create("bob", "alice", 0.8)
        assert cp1.user1 == cp2.user1
        assert cp1.user2 == cp2.user2
        assert cp1.pair_id == cp2.pair_id

    def test_user1_is_smaller(self):
        cp = CandidatePair.create("zoe", "alice", 0.5)
        assert cp.user1 == "alice"
        assert cp.user2 == "zoe"


class TestPairScore:
    def test_to_dict_with_none_scores(self):
        ps = PairScore(
            pair_id="alice__bob",
            user1="alice",
            user2="bob",
            embed_score=0.7,
        )
        d = ps.to_dict()
        assert d["llm_score"] is None
        assert d["llm_score_a_to_b"] is None
        assert d["llm_score_b_to_a"] is None

    def test_to_dict_with_scores(self):
        ps = PairScore(
            pair_id="alice__bob",
            user1="alice",
            user2="bob",
            embed_score=0.7,
            llm_score=0.85,
            llm_score_a_to_b=0.80,
            llm_score_b_to_a=0.90,
        )
        d = ps.to_dict()
        assert d["llm_score"] == 0.85
        assert d["llm_score_a_to_b"] == 0.80
        assert d["llm_score_b_to_a"] == 0.90


class TestPrefMatrix:
    def test_to_dict(self):
        pm = PrefMatrix(
            left_ids=["a", "b"],
            right_ids=["c", "d"],
            pref_left_to_right=np.array([[0.8, 0.3], [0.5, 0.7]]),
            pref_right_to_left=np.array([[0.6, 0.4], [0.2, 0.9]]),
        )
        d = pm.to_dict()
        assert d["left_ids"] == ["a", "b"]
        assert d["right_ids"] == ["c", "d"]


class TestEdge:
    def test_to_dict_roundtrip_fields(self):
        e = Edge(
            user1="alice",
            user2="bob",
            pair_id="alice__bob",
            final_weight=0.863,
            embed_score=0.776,
            llm_score=0.909,
            intro="You should connect...",
            starter_topics="AI, art",
        )
        d = e.to_dict()
        assert d["final_weight"] == 0.863
        assert d["intro"] == "You should connect..."
        assert d["pair_id"] == "alice__bob"


class TestEvaluationReport:
    def test_total_envy(self):
        er = EvaluationReport(
            hr_at_1=0.5,
            hr_at_3=0.7,
            hr_at_5=0.8,
            ndcg_at_5=0.6,
            envy_count_left=1,
            envy_count_right=2,
        )
        assert er.total_envy == 3

    def test_passes_gates_pass(self):
        er = EvaluationReport(
            hr_at_1=0.5,
            hr_at_3=0.7,
            hr_at_5=0.8,
            ndcg_at_5=0.6,
            envy_count_left=0,
            envy_count_right=1,
        )
        assert er.passes_gates({"hr_at_3_min": 0.6, "ndcg_at_5_min": 0.4, "total_envy_max": 2})

    def test_passes_gates_fail_hr(self):
        er = EvaluationReport(
            hr_at_1=0.3,
            hr_at_3=0.5,
            hr_at_5=0.6,
            ndcg_at_5=0.6,
            envy_count_left=0,
            envy_count_right=0,
        )
        assert not er.passes_gates({"hr_at_3_min": 0.6})

    def test_passes_gates_fail_envy(self):
        er = EvaluationReport(
            hr_at_1=0.7,
            hr_at_3=0.8,
            hr_at_5=0.9,
            ndcg_at_5=0.7,
            envy_count_left=2,
            envy_count_right=1,
        )
        assert not er.passes_gates({"total_envy_max": 2})
