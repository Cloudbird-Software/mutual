"""embed stage 单元测试（离线，fake_embedder 契约见 spec/04-fixtures.md §7.2）。

覆盖 spec/02-stages.md §3 与 spec/05-boundaries.md §6：
- content-addressed 增量复用：改一个 profile 只重嵌该变化 cell；
- 不同 embedding_model 的 bundle 整体忽略（迁移守卫）；
- 全尺寸存储，MRL 截断只在工作副本做；
- subset(ids) 原语；缺失 section = 零向量（下游 mask）。
"""

import copy
import dataclasses

import numpy as np
import pytest

from mutual.embed import (
    dump_bundle,
    embed_sections,
    load_bundle,
    supports_mrl,
    truncate_embeddings,
)
from mutual.extract import NOT_SPECIFIED
from mutual.schemas import ExtractedSections, HydeDescriptors, hash_text

USERS = ("alice", "bob", "carol")


class CountingEmbedder:
    """包装 fake_embedder，记录每次调用收到的文本（验证增量复用）。"""

    def __init__(self, inner):
        self.inner = inner
        self.calls = []

    def embed(self, texts):
        self.calls.append(list(texts))
        return self.inner.embed(texts)

    @property
    def all_texts(self):
        return [t for call in self.calls for t in call]


def _sections():
    return [
        ExtractedSections(
            id=uid,
            sections={
                "skills": f"{uid} skills text",
                "vision": f"{uid} vision text",
                "project": f"{uid} project text",
                "needs": f"{uid} needs text",
            },
        )
        for uid in USERS
    ]


def _hyde():
    return {
        uid: HydeDescriptors(id=uid, descriptors={"skills": [f"{uid} hyde skills"]})
        for uid in USERS
    }


def _make_embedder(fake_llm):
    return CountingEmbedder(fake_llm.get_embedding_model())


class TestBundleShape:
    def test_shapes_model_dim(self, config, fake_llm):
        bundle = embed_sections(
            _sections(), _hyde(), config, embedder=fake_llm.get_embedding_model()
        )
        assert bundle.user_ids == list(USERS)
        assert bundle.section_names == sorted(["skills", "vision", "project", "needs"])
        assert bundle.embedding_model == config["models"]["embedding"]
        assert bundle.embeddings.shape == (3, 4, 128)
        assert bundle.dim == 128
        assert bundle.hyde["skills"].shape == (3, 1, 128)

    def test_hash_keys_are_content_addressed(self, config, fake_llm):
        bundle = embed_sections(
            _sections(), _hyde(), config, embedder=fake_llm.get_embedding_model()
        )
        assert bundle.section_hashes["alice|skills"] == hash_text("alice skills text")
        assert bundle.hyde_hashes["alice|skills|0"] == hash_text("alice hyde skills")

    def test_missing_section_is_zero_vector_and_unhashed(self, config, fake_llm):
        sections = _sections()
        sections[0].sections["needs"] = NOT_SPECIFIED
        bundle = embed_sections(sections, _hyde(), config, embedder=fake_llm.get_embedding_model())
        needs_i = bundle.section_names.index("needs")
        assert np.all(bundle.embeddings[0, needs_i] == 0)
        assert "alice|needs" not in bundle.section_hashes
        # 非缺失 cell 正常
        assert "bob|needs" in bundle.section_hashes

    def test_deterministic(self, config, fake_llm):
        e = fake_llm.get_embedding_model()
        b1 = embed_sections(_sections(), _hyde(), config, embedder=e)
        b2 = embed_sections(_sections(), _hyde(), config, embedder=e)
        assert np.array_equal(b1.embeddings, b2.embeddings)
        assert np.array_equal(b1.hyde["skills"], b2.hyde["skills"])


class TestContentAddressedReuse:
    def test_change_one_profile_reembeds_only_that_cell(self, config, fake_llm):
        embedder = _make_embedder(fake_llm)
        bundle1 = embed_sections(_sections(), _hyde(), config, embedder=embedder)
        assert len(embedder.all_texts) == 3 * 4 + 3  # 12 base cells + 3 hyde cells

        # 改 alice 的 skills，其余（含 hyde）输入不变
        sections2 = _sections()
        sections2[0].sections["skills"] = "alice completely new skills"
        embedder2 = _make_embedder(fake_llm)
        bundle2 = embed_sections(sections2, _hyde(), config, existing=bundle1, embedder=embedder2)

        # 只重嵌该 cell（content-addressed，不是 roster-addressed）
        assert embedder2.all_texts == ["alice completely new skills"]

        skills_i = bundle1.section_names.index("skills")
        assert not np.array_equal(bundle2.embeddings[0, skills_i], bundle1.embeddings[0, skills_i])
        for i in range(len(USERS)):
            for j in range(len(bundle1.section_names)):
                if (i, j) == (0, skills_i):
                    continue
                assert np.array_equal(bundle2.embeddings[i, j], bundle1.embeddings[i, j]), (
                    f"cell ({i},{j}) 不应变化"
                )
        # hyde 全部复用
        assert np.array_equal(bundle2.hyde["skills"], bundle1.hyde["skills"])
        expected_new = fake_llm.get_embedding_model().embed(["alice completely new skills"])[0]
        assert np.array_equal(bundle2.embeddings[0, skills_i], expected_new)

    def test_change_one_descriptor_reembeds_only_that_hyde_cell(self, config, fake_llm):
        embedder = _make_embedder(fake_llm)
        bundle1 = embed_sections(_sections(), _hyde(), config, embedder=embedder)

        hyde2 = _hyde()
        hyde2["bob"].descriptors["skills"] = ["bob new hyde descriptor"]
        embedder2 = _make_embedder(fake_llm)
        bundle2 = embed_sections(_sections(), hyde2, config, existing=bundle1, embedder=embedder2)

        assert embedder2.all_texts == ["bob new hyde descriptor"]
        assert np.array_equal(bundle2.embeddings, bundle1.embeddings)
        assert np.array_equal(bundle2.hyde["skills"][0], bundle1.hyde["skills"][0])
        assert not np.array_equal(bundle2.hyde["skills"][1], bundle1.hyde["skills"][1])
        assert np.array_equal(bundle2.hyde["skills"][2], bundle1.hyde["skills"][2])

    def test_full_reuse_makes_no_embedder_call(self, config, fake_llm):
        embedder = _make_embedder(fake_llm)
        bundle1 = embed_sections(_sections(), _hyde(), config, embedder=embedder)
        embedder2 = _make_embedder(fake_llm)
        bundle2 = embed_sections(_sections(), _hyde(), config, existing=bundle1, embedder=embedder2)
        assert embedder2.calls == []
        assert np.array_equal(bundle2.embeddings, bundle1.embeddings)


class TestModelMigrationGuard:
    def test_different_model_bundle_ignored_entirely(self, config, fake_llm):
        embedder = _make_embedder(fake_llm)
        bundle1 = embed_sections(_sections(), _hyde(), config, embedder=embedder)

        wrong = dataclasses.replace(bundle1, embedding_model="some-other-model")
        embedder2 = _make_embedder(fake_llm)
        bundle2 = embed_sections(_sections(), _hyde(), config, existing=wrong, embedder=embedder2)

        # 整体忽略：全部 cell 重嵌
        assert len(embedder2.all_texts) == 3 * 4 + 3
        assert np.array_equal(bundle2.embeddings, bundle1.embeddings)
        assert np.array_equal(bundle2.hyde["skills"], bundle1.hyde["skills"])

    def test_timestamps_preserved_only_on_reuse(self, config, fake_llm):
        embedder = _make_embedder(fake_llm)
        bundle1 = embed_sections(_sections(), _hyde(), config, embedder=embedder)
        stamped = dataclasses.replace(bundle1, user_timestamps={"alice": "2026-08-14T00:00:00Z"})
        bundle2 = embed_sections(
            _sections(), _hyde(), config, existing=stamped, embedder=_make_embedder(fake_llm)
        )
        assert bundle2.user_timestamps.get("alice") == "2026-08-14T00:00:00Z"


class TestEmbedderInjection:
    def test_embedder_from_config_llm_wrapper(self, config, fake_llm):
        cfg = copy.deepcopy(config)
        cfg["llm_wrapper"] = fake_llm
        bundle = embed_sections(_sections(), _hyde(), cfg)
        assert bundle.dim == 128

    def test_missing_embedder_raises(self, config):
        with pytest.raises(ValueError, match="embedder"):
            embed_sections(_sections(), _hyde(), config)


class TestMrl:
    def test_full_size_storage_despite_dimensions_config(self, config, fake_llm):
        cfg = copy.deepcopy(config)
        cfg["models"]["embedding_dimensions"] = 64
        bundle = embed_sections(_sections(), _hyde(), cfg, embedder=fake_llm.get_embedding_model())
        # 全尺寸存储（128），MRL 截断不发生在存储层
        assert bundle.dim == 128
        assert bundle.embeddings.shape[-1] == 128

    def test_truncate_only_on_working_copy(self, config, fake_llm):
        bundle = embed_sections(
            _sections(), _hyde(), config, embedder=fake_llm.get_embedding_model()
        )
        before = bundle.embeddings.copy()
        truncated = truncate_embeddings(bundle.embeddings, 64)

        assert truncated.shape == (3, 4, 64)
        norms = np.linalg.norm(truncated, axis=-1)
        np.testing.assert_allclose(norms, 1.0, atol=1e-10)
        assert np.array_equal(bundle.embeddings, before)  # 原全尺寸未动

    def test_truncate_zero_vector_stays_zero(self):
        vecs = np.zeros((2, 1, 8))
        out = truncate_embeddings(vecs, 4)
        assert np.array_equal(out, np.zeros((2, 1, 4)))

    def test_truncate_invalid_dim_raises(self):
        with pytest.raises(ValueError):
            truncate_embeddings(np.ones((1, 2, 8)), 0)

    def test_supports_mrl(self, config):
        assert supports_mrl("text-embedding-3-small") is True
        assert supports_mrl("text-embedding-3-large") is True
        assert supports_mrl("some-legacy-model") is False
        # 生产配置的 embedding（默认 voyage）不支持 MRL，截断应回退全尺寸
        # （不与被测生产模型耦合；见 src/mutual/embed.py supports_mrl）。
        assert supports_mrl(config["models"]["embedding"]) is False


class TestSubsetAndDump:
    def test_subset(self, config, fake_llm):
        bundle = embed_sections(
            _sections(), _hyde(), config, embedder=fake_llm.get_embedding_model()
        )
        sub = bundle.subset(["carol", "alice"])
        assert sub.user_ids == ["carol", "alice"]
        assert np.array_equal(sub.embeddings[0], bundle.embeddings[2])
        assert np.array_equal(sub.embeddings[1], bundle.embeddings[0])
        assert np.array_equal(sub.hyde["skills"][0], bundle.hyde["skills"][2])

    def test_dump_load_roundtrip(self, config, fake_llm, tmp_path):
        bundle = embed_sections(
            _sections(), _hyde(), config, embedder=fake_llm.get_embedding_model()
        )
        path = str(tmp_path / "bundle.npz")
        dump_bundle(bundle, path)
        loaded = load_bundle(path)

        assert loaded.user_ids == bundle.user_ids
        assert loaded.section_names == bundle.section_names
        assert loaded.embedding_model == bundle.embedding_model
        assert loaded.dim == bundle.dim
        assert np.array_equal(loaded.embeddings, bundle.embeddings)
        assert set(loaded.hyde.keys()) == set(bundle.hyde.keys())
        for name in bundle.hyde:
            assert np.array_equal(loaded.hyde[name], bundle.hyde[name])
        assert loaded.section_hashes == bundle.section_hashes
        assert loaded.hyde_hashes == bundle.hyde_hashes
        assert loaded.user_timestamps == bundle.user_timestamps
