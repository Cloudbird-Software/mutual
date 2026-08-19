"""FileStore 离线单元测试（tmp_path 做根目录，不联网）。

覆盖目录结构、sections/embeddings 往返、失败项不持久化（§4）、
match_history append-only 与 novelty 窗口过滤（§8）。
"""

import json
from datetime import datetime, timedelta, timezone

import numpy as np
import pytest

from mutual.schemas import Edge, EmbeddingsBundle, ExtractedSections, stable_pair_id
from mutual.store import FileStore, Store


@pytest.fixture
def store(config, tmp_path):
    """novelty_window_months 从 config 注入（不硬编码）。"""
    return FileStore(
        root=str(tmp_path / "root"),
        novelty_window_months=config["matching"]["novelty_window_months"],
    )


def _edge(a: str, b: str) -> Edge:
    return Edge(
        user1=min(a, b),
        user2=max(a, b),
        pair_id=stable_pair_id(a, b),
        final_weight=0.8,
        embed_score=0.7,
        llm_score=0.9,
    )


def _make_bundle() -> EmbeddingsBundle:
    rng = np.random.RandomState(42)
    return EmbeddingsBundle(
        user_ids=["alice", "bob"],
        section_names=["skills", "needs"],
        embeddings=rng.randn(2, 2, 8),
        hyde={"skills": rng.randn(2, 1, 8), "needs": rng.randn(2, 1, 8)},
        embedding_model="text-embedding-3-small",
        dim=8,
        section_hashes={"alice|skills": "aaaa", "bob|needs": "bbbb"},
        hyde_hashes={"alice|skills": "cccc"},
        user_timestamps={"alice": "2026-08-14T00:00:00Z"},
    )


class TestDirectoryStructure:
    def test_five_subdirs_created_on_init(self, store):
        assert isinstance(store, Store)
        for d in (
            store.raw_dir,
            store.processed_dir,
            store.embeds_dir,
            store.outputs_dir,
            store.cache_dir,
        ):
            assert d.is_dir(), f"{d} 未创建"

    def test_history_path_at_root(self, store):
        assert store.history_path == store.root / "match_history.jsonl"


class TestSections:
    def test_roundtrip(self, store):
        alice = ExtractedSections(id="alice", sections={"skills": "art", "needs": "tech help"})
        bob = ExtractedSections(id="bob", sections={"skills": "python", "vision": "ai"})
        store.put_sections([alice, bob])

        got_all = store.get_sections()
        assert set(got_all) == {"alice", "bob"}
        assert got_all["alice"].sections == alice.sections
        assert got_all["alice"].hash == alice.hash

        got_bob = store.get_sections(["bob"])
        assert set(got_bob) == {"bob"}
        assert got_bob["bob"].sections == bob.sections

    def test_missing_user_ids_skipped(self, store):
        store.put_sections([ExtractedSections(id="alice", sections={"skills": "art"})])
        got = store.get_sections(["alice", "ghost"])
        assert set(got) == {"alice"}

    def test_put_rejects_path_traversal_ids(self, store):
        """qodo #1：``../`` / 绝对路径 / 分隔符 ID 拒绝持久化（fail-loud）。"""
        evil_ids = ["../escape", "/etc/passwd", "a/b", "a\\b", "..", "."]
        for eid in evil_ids:
            with pytest.raises(ValueError, match="路径穿越守卫"):
                store.put_sections([ExtractedSections(id=eid, sections={"skills": "x"})])

    def test_put_rejects_dot_prefixed_ids(self, store):
        """隐藏文件式 ID（``.env``）同样拒绝。"""
        with pytest.raises(ValueError, match="路径穿越守卫"):
            store.put_sections([ExtractedSections(id=".env", sections={"skills": "x"})])

    def test_get_skips_unsafe_ids_without_io(self, store, tmp_path):
        """qodo #1：读侧不安全 ID 静默跳过，绝不拼入路径（不抛、不读盘外）。"""
        got = store.get_sections(["../../etc/passwd", "a/b"])
        assert got == {}
        assert not (tmp_path / "root" / "processed" / "sections" / "passwd.json").exists()

    def test_put_accepts_safe_id_charset(self, store):
        """合法 ID（字母数字 + ._-）不受影响。"""
        ok = ExtractedSections(id="user_01.founder-B", sections={"skills": "art"})
        store.put_sections([ok])
        assert set(store.get_sections(["user_01.founder-B"])) == {"user_01.founder-B"}

    def test_empty_store_returns_empty_dict(self, store):
        assert store.get_sections() == {}

    def test_failed_extractions_not_persistized(self, store):
        """§4：全 "Not specified" 的失败项不落盘；部分成功项照常写入。"""
        ok = ExtractedSections(id="alice", sections={"skills": "art"})
        partial = ExtractedSections(
            id="bob",
            sections={
                "skills": "python",
                "vision": "Not specified",
                "project": "Not specified",
                "needs": "Not specified",
            },
        )
        failed = ExtractedSections(
            id="david",
            sections={
                "skills": "Not specified",
                "vision": "Not specified",
                "project": "Not specified",
                "needs": "Not specified",
            },
        )
        store.put_sections([ok, partial, failed])
        assert set(store.get_sections()) == {"alice", "bob"}


class TestEmbeddings:
    def test_roundtrip(self, store):
        bundle = _make_bundle()
        store.put_embeddings(bundle)
        loaded = store.get_embeddings()
        assert loaded is not None
        assert loaded.user_ids == bundle.user_ids
        assert loaded.section_names == bundle.section_names
        assert loaded.embedding_model == bundle.embedding_model
        assert loaded.dim == bundle.dim
        assert loaded.section_hashes == bundle.section_hashes
        assert loaded.hyde_hashes == bundle.hyde_hashes
        assert loaded.user_timestamps == bundle.user_timestamps
        assert np.array_equal(loaded.embeddings, bundle.embeddings)
        assert set(loaded.hyde) == set(bundle.hyde)
        for section in bundle.hyde:
            assert np.array_equal(loaded.hyde[section], bundle.hyde[section])

    def test_returns_none_when_absent(self, store):
        assert store.get_embeddings() is None


class TestMatchHistory:
    def test_put_matches_appends_jsonl(self, store):
        store.put_matches([_edge("alice", "bob")])
        store.put_matches([_edge("alice", "carol"), _edge("bob", "carol")])
        lines = store.history_path.read_text(encoding="utf-8").strip().splitlines()
        assert len(lines) == 3  # append-only
        for line in lines:
            record = json.loads(line)
            assert set(record) == {"pair_id", "user1", "user2", "matched_at"}
            # matched_at 可解析（ISO 8601）
            datetime.fromisoformat(record["matched_at"].replace("Z", "+00:00"))
        first = json.loads(lines[0])
        assert first["pair_id"] == "alice__bob"
        assert first["user1"] == "alice"
        assert first["user2"] == "bob"

    def test_written_matches_within_window(self, store):
        store.put_matches([_edge("alice", "bob")])
        history = store.get_match_history()
        assert len(history) == 1
        assert history[0]["pair_id"] == "alice__bob"

    def test_empty_history_when_no_file(self, store):
        assert store.get_match_history() == []

    def test_novelty_window_filter(self, store):
        """窗口外记录被过滤；matched_at 不可解析的记录保守保留（S10）。"""
        now = datetime.now(timezone.utc)
        rows = [
            json.dumps(
                {
                    "pair_id": "alice__bob",
                    "user1": "alice",
                    "user2": "bob",
                    "matched_at": (now - timedelta(days=5)).isoformat(),
                }
            ),
            json.dumps(
                {
                    "pair_id": "alice__carol",
                    "user1": "alice",
                    "user2": "carol",
                    "matched_at": (now - timedelta(days=200)).isoformat(),
                }
            ),
            json.dumps(
                {
                    "pair_id": "bob__carol",
                    "user1": "bob",
                    "user2": "carol",
                    "matched_at": "not-a-timestamp",
                }
            ),
            "{broken json",
        ]
        store.history_path.write_text("\n".join(rows) + "\n", encoding="utf-8")

        pair_ids = {record["pair_id"] for record in store.get_match_history()}
        # config 默认窗口 6 个月：5 天前保留、200 天前过滤、坏时间戳保守保留
        assert pair_ids == {"alice__bob", "bob__carol"}


class TestStoreProtocol:
    def test_store_is_abstract(self):
        with pytest.raises(TypeError):
            Store()
