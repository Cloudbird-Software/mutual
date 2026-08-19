"""runners 三模式测试：monkeypatch 注册表条目（extract/hyde/embed/similarity/select）
+ fake_llm，全内存离线运行；match 阶段保持 stub → 验证兜底路径。
"""

import copy

import numpy as np
import pytest

from mutual import runners, stages
from mutual.schemas import (
    CandidatePair,
    Edge,
    EmbeddingsBundle,
    ExtractedSections,
    HydeDescriptors,
    Profile,
    SimilarityResult,
    hash_text,
)
from mutual.store import Store

_DIM = 8
_SECTION_NAMES = ["needs", "project", "skills", "vision"]


# ---------------------------------------------------------------------------
# 轻量假实现（模拟其他 agent 职责的 stage）
# ---------------------------------------------------------------------------


def fake_extract(profiles, config, llm_wrapper, failed_out=None):
    return [ExtractedSections(id=p.id, sections=dict(p.sections)) for p in profiles]


def fake_hyde(sections, config, llm_wrapper):
    return {
        es.id: HydeDescriptors(id=es.id, descriptors={k: [v] for k, v in es.sections.items()})
        for es in sections
    }


def fake_embed(sections, hyde, config, existing=None):
    names = sorted(sections[0].sections.keys()) if sections else []
    n = len(sections)
    emb = np.zeros((n, len(names), _DIM))
    for i, es in enumerate(sections):
        for j, name in enumerate(names):
            rng = np.random.RandomState(int(hash_text(f"{es.id}|{name}"), 16) % 2**32)
            v = rng.randn(_DIM)
            v[0] = abs(v[0]) + 2.0  # 共同正向轴：保证余弦为正（select 只留正相似度对）
            emb[i, j] = v / np.linalg.norm(v)
    return EmbeddingsBundle(
        user_ids=[es.id for es in sections],
        section_names=names,
        embeddings=emb,
        hyde={name: np.zeros((n, 1, _DIM)) for name in names},
        embedding_model="fake-embedder",
        dim=_DIM,
    )


def fake_similarity(source, target, recipe_config):
    src = source.embeddings.mean(axis=1)
    if target is None:
        tgt, target_ids = src, list(source.user_ids)
    else:
        tgt, target_ids = target.embeddings.mean(axis=1), list(target.user_ids)
    m = src @ tgt.T
    return SimilarityResult(list(source.user_ids), target_ids, m.copy(), m.copy())


def fake_select(similarity, budgets, excluded_pairs=None):
    pairs, seen = [], set()
    for i, sid in enumerate(similarity.source_ids):
        for j, tid in enumerate(similarity.target_ids):
            if sid == tid:
                continue
            if similarity.is_square and sid > tid:
                continue
            score = float(similarity.fused_matrix[i, j])
            if score <= 0:
                continue
            cp = CandidatePair.create(sid, tid, score)
            if cp.pair_id in seen:
                continue
            if excluded_pairs and cp.pair_id in excluded_pairs:
                continue
            seen.add(cp.pair_id)
            pairs.append(cp)
    pairs.sort(key=lambda p: (-p.similarity_score, p.pair_id))
    return pairs


def install_fake_stages(monkeypatch, extract_fn=None):
    """把轻量假实现装进注册表（本仓库其他 stage 仍是 stub）。"""
    overrides = {
        "extract": extract_fn or fake_extract,
        "hyde": fake_hyde,
        "embed": fake_embed,
        "similarity": fake_similarity,
        "select": fake_select,
    }
    for name, fn in overrides.items():
        monkeypatch.setattr(stages.get_stage(name), "run", fn)


class MemoryStore(Store):
    """全内存 Store 假实现（验证 runners 的 IO 边界）。"""

    def __init__(self):
        self.sections = {}
        self.bundle = None
        self.history = []
        self.put_sections_calls = []
        self.put_embeddings_calls = 0
        self.put_matches_calls = 0

    def get_sections(self, user_ids=None):
        if user_ids is None:
            return dict(self.sections)
        return {uid: self.sections[uid] for uid in user_ids if uid in self.sections}

    def put_sections(self, extracted):
        self.put_sections_calls.append(list(extracted))
        for es in extracted:
            self.sections[es.id] = es

    def get_embeddings(self):
        return self.bundle

    def put_embeddings(self, bundle):
        self.put_embeddings_calls += 1
        self.bundle = bundle

    def get_match_history(self):
        return list(self.history)

    def put_matches(self, edges):
        self.put_matches_calls += 1
        for e in edges:
            self.history.append(
                {"pair_id": e.pair_id, "user1": e.user1, "user2": e.user2, "matched_at": "now"}
            )


# ---------------------------------------------------------------------------
# fixtures / helpers
# ---------------------------------------------------------------------------


def _profiles():
    return [
        Profile(id=uid, sections={name: f"{uid} {name}" for name in _SECTION_NAMES})
        for uid in ("alice", "bob", "carol", "david")
    ]


def _config(config):
    cfg = copy.deepcopy(config)
    cfg["budgets"]["n_profiles_to_score_together"] = 1  # fake 按单对查表打分
    return cfg


@pytest.fixture
def runner_config(config):
    return _config(config)


# ---------------------------------------------------------------------------
# run_full_match
# ---------------------------------------------------------------------------


class TestRunFullMatch:
    def test_end_to_end_real_match(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        result = runners.run_full_match(_profiles(), runner_config, llm_wrapper=fake_llm)

        assert len(result.edges) == 6  # 4 人完全图
        assert result.envy_report is not None  # match 已实现 → 有 envy 报告
        assert result.envy_report["total_envy"] == 0
        assert "match 阶段未实现" not in "".join(result.report_data["notes"])

        weights = [e.final_weight for e in result.edges]
        assert weights == sorted(weights, reverse=True)
        for edge in result.edges:
            assert edge.intro == "Fake intro."
            assert edge.starter_topics == "Fake topic."
            assert edge.llm_score_a_to_b is not None

        ov = result.report_data["overview"]
        assert ov["total_users"] == 4
        assert ov["total_edges"] == 6
        assert ov["edges_with_directional_scores"] == 6
        assert result.report_data["users"]["alice"]["degree"] == 3

        # llm 方向分统计与 spec/04-fixtures.md §7.1 表自洽
        llm = result.report_data["score_statistics"]["llm_scores"]
        assert (llm["min"], llm["max"], llm["avg"]) == (0.35, 0.9, 0.683)

        assert {p["pair_id"] for p in result.new_pairs} == {e.pair_id for e in result.edges}

    def test_direction_asymmetry_flows_to_edges(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        result = runners.run_full_match(_profiles(), runner_config, llm_wrapper=fake_llm)
        by_id = {e.pair_id: e for e in result.edges}
        assert by_id["alice__bob"].llm_score_a_to_b == pytest.approx(0.85)
        assert by_id["alice__bob"].llm_score_b_to_a == pytest.approx(0.90)
        assert by_id["carol__david"].llm_score_a_to_b == pytest.approx(0.35)

    def test_determinism(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        r1 = runners.run_full_match(_profiles(), runner_config, llm_wrapper=fake_llm)
        r2 = runners.run_full_match(_profiles(), runner_config, llm_wrapper=fake_llm)
        assert [e.to_dict() for e in r1.edges] == [e.to_dict() for e in r2.edges]

    def test_bundle_input_skips_extract(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        extracted = fake_extract(_profiles(), runner_config, fake_llm)
        bundle = fake_embed(extracted, fake_hyde(extracted, runner_config, fake_llm), runner_config)

        calls = []

        def tracking_extract(profiles, config, llm_wrapper, failed_out=None):
            calls.append(1)
            return fake_extract(profiles, config, llm_wrapper, failed_out)

        install_fake_stages(monkeypatch, extract_fn=tracking_extract)
        result = runners.run_full_match(
            bundle, runner_config, llm_wrapper=fake_llm, extracted=extracted
        )
        assert calls == []
        assert len(result.edges) == 6

    def test_min_profiles_required(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        with pytest.raises(ValueError, match="min_profiles_required"):
            runners.run_full_match(_profiles()[:1], runner_config, llm_wrapper=fake_llm)

    def test_llm_wrapper_required(self, monkeypatch, runner_config):
        install_fake_stages(monkeypatch)
        with pytest.raises(ValueError, match="llm_wrapper"):
            runners.run_full_match(_profiles(), runner_config)

    def test_store_io(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        store = MemoryStore()
        runners.run_full_match(_profiles(), runner_config, store=store, llm_wrapper=fake_llm)

        assert len(store.put_sections_calls[0]) == 4  # 全部提取成功 → 全持久化
        assert store.put_embeddings_calls == 1
        assert store.put_matches_calls == 1
        assert len(store.history) == 6

        # 第二次运行：history 内全部 pair 被 novelty 排除（§8）→ 无边
        result2 = runners.run_full_match(
            _profiles(), runner_config, store=store, llm_wrapper=fake_llm
        )
        assert result2.edges == []
        assert result2.report_data["overview"]["total_edges"] == 0

    def test_match_stage_used_when_registered(self, monkeypatch, fake_llm, runner_config):
        """match 阶段实现后走注册表，不走兜底。"""
        install_fake_stages(monkeypatch)
        envy = {"left_envy_count": 0, "right_envy_count": 0, "total_envy": 0}
        match_prob = np.ones((4, 4)) / 16.0

        def fake_match(pref_matrix, matching_config, blending_config, reference_scores):
            edge = Edge(
                user1="alice",
                user2="bob",
                pair_id="alice__bob",
                final_weight=0.9,
                embed_score=0.5,
                llm_score=0.8,
            )
            return [edge], match_prob, envy

        monkeypatch.setattr(stages.get_stage("match"), "run", fake_match)
        result = runners.run_full_match(_profiles(), runner_config, llm_wrapper=fake_llm)
        assert len(result.edges) == 1
        assert result.envy_report == envy
        assert result.edges[0].intro == "Fake intro."
        assert "match 阶段未实现" not in "".join(result.report_data.get("notes", []))


# ---------------------------------------------------------------------------
# run_query_match
# ---------------------------------------------------------------------------


class TestRunQueryMatch:
    def _pool(self):
        extracted = fake_extract(_profiles()[:3], {}, None)
        bundle = fake_embed(extracted, fake_hyde(extracted, {}, None), {})
        return extracted, bundle

    def test_query_edges_only_to_pool(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        pool_extracted, pool_bundle = self._pool()
        result = runners.run_query_match(
            "looking for an art collaborator",
            pool_bundle,
            runner_config,
            llm_wrapper=fake_llm,
            pool_sections=pool_extracted,
        )
        assert result.edges
        for edge in result.edges:
            assert "query" in (edge.user1, edge.user2)
            other = edge.user2 if edge.user1 == "query" else edge.user1
            assert other in pool_bundle.user_ids

    def test_query_report_scoped_to_query_user(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        pool_extracted, pool_bundle = self._pool()
        result = runners.run_query_match(
            "text",
            pool_bundle,
            runner_config,
            llm_wrapper=fake_llm,
            pool_sections=pool_extracted,
        )
        assert set(result.report_data["users"]) == {"query"}
        matches = result.report_data["users"]["query"]["matches"]
        assert matches, "query 用户应至少有一条匹配"
        weights = [m["weight"] for m in matches]
        assert weights == sorted(weights, reverse=True)
        # query 非 cohort 用户 → fake 默认对称 0.5/0.5（§7.1 默认兜底）
        for m in matches:
            assert m["directional_scores"] == {"a_to_b": 0.5, "b_to_a": 0.5}

    def test_query_custom_id(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        pool_extracted, pool_bundle = self._pool()
        result = runners.run_query_match(
            "text",
            pool_bundle,
            runner_config,
            llm_wrapper=fake_llm,
            pool_sections=pool_extracted,
            query_id="zoe",
        )
        assert set(result.report_data["users"]) == {"zoe"}
        assert all("zoe" in (e.user1, e.user2) for e in result.edges)


# ---------------------------------------------------------------------------
# run_batch_match
# ---------------------------------------------------------------------------


class TestRunBatchMatch:
    def _pool(self):
        extracted = fake_extract(_profiles(), {}, None)
        bundle = fake_embed(extracted, fake_hyde(extracted, {}, None), {})
        return extracted, bundle

    def test_batch_scope_members_only(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        pool_extracted, pool_bundle = self._pool()
        batch = runners.run_batch_match(
            ["alice", "carol"],
            pool_bundle,
            runner_config,
            llm_wrapper=fake_llm,
            pool_sections=pool_extracted,
        )
        assert batch.member_ids == ["alice", "carol"]
        assert batch.pool_ids == ["alice", "bob", "carol", "david"]
        assert batch.excluded_pair_ids == []
        assert batch.metadata["match_fallback"] is False  # match 已实现 → 走 NSW 求解

        report = batch.match_result.report_data
        assert set(report["users"]) == {"alice", "carol"}
        # 候选：alice×{bob,carol,david} + carol×{bob,david} = 5（alice__carol 去重）
        assert report["overview"]["total_edges"] == 5

    def test_batch_excluded_pairs(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        pool_extracted, pool_bundle = self._pool()
        batch = runners.run_batch_match(
            ["alice", "carol"],
            pool_bundle,
            runner_config,
            excluded_pairs={"alice__bob", "carol__david"},
            llm_wrapper=fake_llm,
            pool_sections=pool_extracted,
        )
        assert batch.excluded_pair_ids == ["alice__bob", "carol__david"]
        pair_ids = {e.pair_id for e in batch.match_result.edges}
        assert "alice__bob" not in pair_ids
        assert "carol__david" not in pair_ids
        assert len(pair_ids) == 3

    def test_batch_member_member_pair_allowed(self, monkeypatch, fake_llm, runner_config):
        """member⊂pool：member 之间的对（alice__carol）是合法候选（spec 未禁止）。"""
        install_fake_stages(monkeypatch)
        pool_extracted, pool_bundle = self._pool()
        batch = runners.run_batch_match(
            ["alice", "carol"],
            pool_bundle,
            runner_config,
            llm_wrapper=fake_llm,
            pool_sections=pool_extracted,
        )
        assert "alice__carol" in {e.pair_id for e in batch.match_result.edges}

    def test_batch_scored_directional_scores(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        pool_extracted, pool_bundle = self._pool()
        batch = runners.run_batch_match(
            ["alice"],
            pool_bundle,
            runner_config,
            llm_wrapper=fake_llm,
            pool_sections=pool_extracted,
        )
        by_id = {e.pair_id: e for e in batch.match_result.edges}
        assert by_id["alice__bob"].llm_score_a_to_b == pytest.approx(0.85)
        assert by_id["alice__david"].llm_score_b_to_a == pytest.approx(0.63)

    def test_batch_determinism(self, monkeypatch, fake_llm, runner_config):
        install_fake_stages(monkeypatch)
        pool_extracted, pool_bundle = self._pool()
        common = dict(llm_wrapper=fake_llm, pool_sections=pool_extracted)
        b1 = runners.run_batch_match(["alice", "carol"], pool_bundle, runner_config, **common)
        b2 = runners.run_batch_match(["alice", "carol"], pool_bundle, runner_config, **common)
        assert b1.match_result.to_dict() == b2.match_result.to_dict()
