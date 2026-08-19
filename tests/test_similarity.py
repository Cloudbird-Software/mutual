"""similarity stage 单元测试（离线，手造正交向量做精确断言）。

覆盖 spec/02-stages.md §4 与 spec/05-boundaries.md §1、§2：
- 缺失 section = 中性（mask + 分母修正），不是零；
- 方向性不盲目对称化（needs_skills 是方向性的）；
- N×N 方阵 legacy 路径 (dir+dir.T)/2 对称化；M×N 矩形路径不对称化；
- HyDE descriptor pairs max-pool（stages.py hyde notes）。
"""

import numpy as np
import pytest

from mutual.schemas import EmbeddingsBundle
from mutual.similarity import compute_similarity

DIM = 3
E1 = np.array([1.0, 0.0, 0.0])
E2 = np.array([0.0, 1.0, 0.0])
E3 = np.array([0.0, 0.0, 1.0])

RECIPE = {
    "section_weights": {"skills": 1.0, "needs": 1.0},
    "cross_section_weights": {"needs_skills": 2.0},
}


def make_bundle(ids, sections, hyde=None):
    names = sorted(sections)
    emb = np.zeros((len(ids), len(names), DIM))
    for i, uid in enumerate(ids):
        for k, name in enumerate(names):
            vec = sections[name].get(uid)
            if vec is not None:
                emb[i, k] = vec
    return EmbeddingsBundle(
        user_ids=ids,
        section_names=names,
        embeddings=emb,
        hyde=hyde or {},
        embedding_model="fake",
        dim=DIM,
    )


class TestDirectionality:
    def test_needs_skills_is_directional(self):
        # alice: skills=e1, needs=e2；bob: skills=e2, needs=e3
        bundle = make_bundle(
            ["alice", "bob"],
            {"skills": {"alice": E1, "bob": E2}, "needs": {"alice": E2, "bob": E3}},
        )
        result = compute_similarity(bundle, None, RECIPE)

        # dir[alice→bob]：skills cos(e1,e2)=0；needs cos(e2,e3)=0；
        # cross cos(alice.needs=e2, bob.skills=e2)=1 → (2*1)/(1+1+2)=0.5
        assert result.dir_matrix[0, 1] == pytest.approx(0.5)
        # dir[bob→alice]：三项 cos 全 0 → 0（不对称！）
        assert result.dir_matrix[1, 0] == pytest.approx(0.0)
        assert result.dir_matrix[0, 1] != result.dir_matrix[1, 0]

    def test_default_config_recipe_end_to_end(self, config):
        sections = {
            "skills": {"alice": E1, "bob": E2},
            "vision": {"alice": E2, "bob": E3},
            "project": {"alice": E3, "bob": E1},
            "needs": {"alice": E2, "bob": E1},
        }
        bundle = make_bundle(["alice", "bob"], sections)
        result = compute_similarity(bundle, None, config["recipe"])
        assert result.dir_matrix.shape == (2, 2)
        # 用默认 recipe 权重手算 alice→bob：
        # skills -0.1*0 + vision 0.35*0 + project 0.25*0 + needs -0.1*0
        #   + cross 0.8*cos(e2, e2)=0.8 → 0.8 / (−0.1+0.35+0.25−0.1+0.8)=0.8/1.2
        assert result.dir_matrix[0, 1] == pytest.approx(0.8 / 1.2)
        # bob→alice 的 cross = cos(e1, e1) = 1 → 0.8/1.2
        assert result.dir_matrix[1, 0] == pytest.approx(0.8 / 1.2)


class TestMissingSectionNeutral:
    def test_mask_and_denominator_correction(self):
        # carol 缺 needs（零向量）：dir[alice→carol] 只剩 skills 项 + cross 项
        bundle = make_bundle(
            ["alice", "carol"],
            {
                "skills": {"alice": E1, "carol": E2},
                "needs": {"alice": E2, "carol": None},
            },
        )
        result = compute_similarity(bundle, None, RECIPE)

        # skills cos(e1,e2)=0；cross cos(alice.needs=e2, carol.skills=e2)=1；
        # 分母 = 1 + 2 = 3（needs 项被 mask）→ 2/3
        assert result.dir_matrix[0, 1] == pytest.approx(2.0 / 3.0)
        # 中性 ≠ 零：若缺失按 cos=0 计入满分母，会得 2/4=0.5
        assert result.dir_matrix[0, 1] != pytest.approx(0.5)
        # 反方向：carol 的 needs 缺失 → cross masked，只剩 skills 项 → 0/1
        assert result.dir_matrix[1, 0] == pytest.approx(0.0)

    def test_all_sections_missing_user_is_fully_neutral(self):
        # dave 全零：任意方向的相似度 = 0，且不拉低他人
        bundle = make_bundle(
            ["alice", "dave"],
            {"skills": {"alice": E1, "dave": None}, "needs": {"alice": E2, "dave": None}},
        )
        result = compute_similarity(bundle, None, RECIPE)
        assert np.all(result.dir_matrix[0, 1] == 0.0)
        assert np.all(result.dir_matrix[1, 0] == 0.0)
        assert np.all(result.dir_matrix[1, 1] == 0.0)

    def test_unweighted_section_does_not_participate(self):
        # bundle 含 vision，但 recipe 未给 vision 权重 → 不影响结果
        with_vision = make_bundle(
            ["alice", "bob"],
            {
                "skills": {"alice": E1, "bob": E2},
                "needs": {"alice": E2, "bob": E3},
                "vision": {"alice": E3, "bob": E3},
            },
        )
        without_vision = make_bundle(
            ["alice", "bob"],
            {"skills": {"alice": E1, "bob": E2}, "needs": {"alice": E2, "bob": E3}},
        )
        r1 = compute_similarity(with_vision, None, RECIPE)
        r2 = compute_similarity(without_vision, None, RECIPE)
        np.testing.assert_allclose(r1.dir_matrix, r2.dir_matrix)


class TestSquareLegacyPath:
    def test_square_fused_is_symmetrized_dir(self):
        bundle = make_bundle(
            ["alice", "bob"],
            {"skills": {"alice": E1, "bob": E2}, "needs": {"alice": E2, "bob": E3}},
        )
        result = compute_similarity(bundle, None, RECIPE)

        expected = (result.dir_matrix + result.dir_matrix.T) / 2.0
        assert np.array_equal(result.fused_matrix, expected)
        assert np.array_equal(result.fused_matrix, result.fused_matrix.T)
        assert result.source_ids == result.target_ids == ["alice", "bob"]

    def test_rect_fused_equals_dir_no_symmetrization(self):
        src = make_bundle(["alice"], {"skills": {"alice": E1}, "needs": {"alice": E2}})
        tgt = make_bundle(
            ["bob", "carol"],
            {"skills": {"bob": E2, "carol": E1}, "needs": {"bob": E3, "carol": E3}},
        )
        result = compute_similarity(src, tgt, RECIPE)

        assert result.dir_matrix.shape == (1, 2)
        assert np.array_equal(result.fused_matrix, result.dir_matrix)
        assert result.source_ids == ["alice"]
        assert result.target_ids == ["bob", "carol"]
        # alice→bob cross=1 → 0.5；alice→carol skills cos=1, cross cos(e2,e1)=0 → 1/4
        assert result.dir_matrix[0, 0] == pytest.approx(0.5)
        assert result.dir_matrix[0, 1] == pytest.approx(0.25)


class TestHydeMaxPool:
    def test_descriptor_pairs_max_pool(self):
        # erin 的 skills 原向量 = e1，HyDE 描述符向量 = e2；frank skills = e2
        hyde = {"skills": np.array([[[0.0, 1.0, 0.0]], [[0.0, 0.0, 0.0]]])}
        bundle = make_bundle(["erin", "frank"], {"skills": {"erin": E1, "frank": E2}}, hyde=hyde)
        recipe = {"section_weights": {"skills": 1.0}, "cross_section_weights": {}}

        with_hyde = compute_similarity(bundle, None, recipe)
        # max-pool：cos(e1,e2)=0 但 cos(hyde_e2, e2)=1 → 1.0
        assert with_hyde.dir_matrix[0, 1] == pytest.approx(1.0)

        no_hyde = compute_similarity(
            make_bundle(["erin", "frank"], {"skills": {"erin": E1, "frank": E2}}),
            None,
            recipe,
        )
        assert no_hyde.dir_matrix[0, 1] == pytest.approx(0.0)


class TestDeterminism:
    def test_same_input_bitwise_reproducible(self):
        bundle = make_bundle(
            ["alice", "bob"],
            {"skills": {"alice": E1, "bob": E2}, "needs": {"alice": E2, "bob": E3}},
        )
        r1 = compute_similarity(bundle, None, RECIPE)
        r2 = compute_similarity(bundle, None, RECIPE)
        assert np.array_equal(r1.dir_matrix, r2.dir_matrix)
        assert np.array_equal(r1.fused_matrix, r2.fused_matrix)
