"""report 阶段测试：top-N 排序、scope_user_ids、群组摘要与分数统计。"""

from mutual.report import create_report
from mutual.schemas import Edge, ExtractedSections

# spec/04-fixtures.md §7.1 fake 分数表（12 个方向分：min 0.35 / max 0.9 / avg 0.683）
_TABLE = {
    ("alice", "bob"): (0.85, 0.90),
    ("alice", "carol"): (0.80, 0.82),
    ("bob", "carol"): (0.83, 0.82),
    ("alice", "david"): (0.52, 0.63),
    ("bob", "david"): (0.45, 0.58),
    ("carol", "david"): (0.35, 0.65),
}


def _edges_from_table():
    edges = []
    for (u1, u2), (a, b) in _TABLE.items():
        edges.append(
            Edge(
                user1=u1,
                user2=u2,
                pair_id=f"{u1}__{u2}",
                final_weight=(a + b) / 2,
                embed_score=0.58,
                llm_score=(a + b) / 2,
                llm_score_a_to_b=a,
                llm_score_b_to_a=b,
            )
        )
    return edges


def _extracted(*uids):
    return [ExtractedSections(id=uid, sections={"skills": "s"}) for uid in uids]


class TestBasicReport:
    def test_overview_matches_cohort_shape(self):
        edges = _edges_from_table()
        report = create_report(edges, _extracted("alice", "bob", "carol", "david"), 10)
        ov = report["overview"]
        assert ov["total_users"] == 4
        assert ov["total_edges"] == 6
        assert ov["average_degree"] == 3.0
        assert ov["edges_with_llm_scores"] == 6
        assert ov["edges_with_directional_scores"] == 6

    def test_degree_distribution(self):
        report = create_report(
            _edges_from_table(), _extracted("alice", "bob", "carol", "david"), 10
        )
        assert report["degree_distribution"] == {"3": 4}

    def test_users_sorted_by_weight_desc(self):
        report = create_report(
            _edges_from_table(), _extracted("alice", "bob", "carol", "david"), 10
        )
        alice_matches = report["users"]["alice"]["matches"]
        weights = [m["weight"] for m in alice_matches]
        assert weights == sorted(weights, reverse=True)
        assert alice_matches[0]["partner"] == "bob"  # (0.85+0.90)/2 最高
        assert alice_matches[0]["directional_scores"] == {"a_to_b": 0.85, "b_to_a": 0.9}

    def test_top_matches_per_user_caps_list(self):
        report = create_report(_edges_from_table(), _extracted("alice", "bob", "carol", "david"), 2)
        for info in report["users"].values():
            assert len(info["matches"]) <= 2
            assert info["degree"] == 3  # degree 仍是真实度数，不受 top-N 截断

    def test_user_without_edges_reported(self):
        report = create_report([], _extracted("alice", "bob"), 5)
        assert report["overview"]["total_edges"] == 0
        assert report["users"]["alice"]["degree"] == 0
        assert report["users"]["alice"]["matches"] == []

    def test_purity_edges_unchanged(self):
        edges = _edges_from_table()
        create_report(edges, _extracted("alice", "bob", "carol", "david"), 1)
        assert len(edges) == 6
        assert edges[0].final_weight == (0.85 + 0.90) / 2


class TestScoreStatistics:
    def test_llm_stats_over_directional_values_match_fixture(self):
        """与 cohort.json 的 llm_scores 统计（min 0.35/max 0.9/avg 0.683）自洽。"""
        report = create_report(
            _edges_from_table(), _extracted("alice", "bob", "carol", "david"), 10
        )
        llm = report["score_statistics"]["llm_scores"]
        assert llm["min"] == 0.35
        assert llm["max"] == 0.9
        assert llm["avg"] == 0.683

    def test_final_and_embed_stats(self):
        report = create_report(
            _edges_from_table(), _extracted("alice", "bob", "carol", "david"), 10
        )
        final = report["score_statistics"]["final_weights"]
        assert final["min"] == round((0.35 + 0.65) / 2, 3)  # carol__david 最低
        assert final["max"] == round((0.85 + 0.90) / 2, 3)  # alice__bob 最高
        assert report["score_statistics"]["embedding_scores"]["avg"] == 0.58

    def test_unscored_edges_llm_stats_fallback_to_fused(self):
        edges = [
            Edge(
                user1="alice",
                user2="bob",
                pair_id="alice__bob",
                final_weight=0.5,
                embed_score=0.5,
                llm_score=0.7,
            ),
        ]
        report = create_report(edges, _extracted("alice", "bob"), 5)
        assert report["score_statistics"]["llm_scores"] == {"min": 0.7, "max": 0.7, "avg": 0.7}
        assert report["overview"]["edges_with_llm_scores"] == 0

    def test_empty_edges_stats_are_none(self):
        report = create_report([], _extracted("alice"), 5)
        assert report["score_statistics"]["final_weights"] == {
            "min": None,
            "max": None,
            "avg": None,
        }


class TestScopeUserIds:
    def test_scope_limits_user_reports(self):
        edges = _edges_from_table()
        report = create_report(
            edges,
            _extracted("alice", "bob", "carol", "david"),
            10,
            scope_user_ids=["alice", "carol"],
        )
        assert set(report["users"]) == {"alice", "carol"}
        # 边统计只算与 scoped 用户相邻的边（batch 模式 member 侧语义）；
        # alice 邻接 {ab,ac,ad}，carol 邻接 {ac,bc,cd}，并集 5 条（bob__david 除外）
        assert report["overview"]["total_users"] == 2
        assert report["overview"]["total_edges"] == 5
        partners = {m["partner"] for m in report["users"]["alice"]["matches"]}
        assert partners == {"bob", "carol", "david"}  # 对端可作为 partner 出现

    def test_scope_excludes_irrelevant_edges(self):
        edges = _edges_from_table()
        report = create_report(
            edges, _extracted("alice", "bob", "carol", "david"), 10, scope_user_ids=["david"]
        )
        # david 邻接 3 条边；alice__bob / bob__carol / alice__carol 不计入
        assert report["overview"]["total_edges"] == 3
        assert report["overview"]["average_degree"] == 3.0
        assert report["degree_distribution"] == {"3": 1}

    def test_scope_empty(self):
        report = create_report(_edges_from_table(), _extracted("alice"), 5, scope_user_ids=[])
        assert report["users"] == {}
        assert report["overview"]["total_edges"] == 0

    def test_scope_respects_top_n(self):
        report = create_report(
            _edges_from_table(),
            _extracted("alice", "bob", "carol", "david"),
            1,
            scope_user_ids=["alice"],
        )
        assert len(report["users"]["alice"]["matches"]) == 1
