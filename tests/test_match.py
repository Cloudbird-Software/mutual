"""match / evaluate 的 NSW 求解与 envy 语义回归测试（spec/02-stages.md §8、
spec/03-oracles.md §2、spec/05-boundaries.md §7）。

覆盖四个契约点：
1. 同集（cohort）匹配的 ``match_prob`` 必须对称存储（无向匹配），
   envy 检查基于**完整匹配集**（own-best 语义），不得因字典覆盖丢失多匹配。
2. ``evaluate`` 的 envy 计数与 ``check_envy`` 语义一致（同输入同结果）。
3. 同集贪心必须按 NSW 分数降序（全局互惠最优意图，engineering-plan §4.1），
   不得按索引序。
4. batch 模式（member×pool 二部图）：``b_max`` 只绑定 member 侧，
   ``pool_b_max=None`` 时 pool 侧不限度（spec/05-boundaries.md §7）。
"""

import copy

import numpy as np
import pytest

from mutual import runners
from mutual.evaluate import evaluate
from mutual.match import check_envy, solve_match
from mutual.schemas import PrefMatrix, Profile


def _square(ids, pref):
    """构造同集 PrefMatrix（pref[i,j] = i 对 j 的偏好，双向矩阵同值，
    与 score.build_pref_matrix 的同集输出一致）。"""
    p = np.asarray(pref, dtype=float)
    return PrefMatrix(
        left_ids=list(ids),
        right_ids=list(ids),
        pref_left_to_right=p.copy(),
        pref_right_to_left=p.copy(),
    )


_BLEND = {"embed_weight": 0.5, "llm_weight": 0.5}


# ---------------------------------------------------------------------------
# 1. envy 检查必须基于完整匹配集（不得丢多匹配）
# ---------------------------------------------------------------------------


def test_check_envy_uses_full_match_sets():
    """u0 匹配 {u1, u2}，own-best = 0.9；他人 bundle 中最高 0.8 < 0.9 → 无 envy。

    若 check_envy 用 dict 覆盖只保留最后一个 partner（u0→u2，0.1），
    会误报 u0 嫉妒（0.8 > 0.1）→ false positive。
    """
    ids = ["u0", "u1", "u2", "u3"]
    pref = [
        [0.0, 0.9, 0.1, 0.8],  # u0: 最爱 u1(0.9)，被 u3 的 0.8 不构成 envy
        [0.5, 0.0, 0.0, 0.5],
        [0.5, 0.0, 0.0, 0.5],
        [0.0, 0.5, 0.5, 0.0],
    ]
    pm = _square(ids, pref)

    edges, match_prob, envy = solve_match(pm, {"b_max": 2}, _BLEND)

    # 期望匹配：u0-{u1,u2}, u3-{u1,u2}（u0__u3 与 u1__u2 的 NSW 为 0 不匹配）
    pairs = {tuple(sorted((e.user1, e.user2))) for e in edges}
    assert pairs == {("u0", "u1"), ("u0", "u2"), ("u1", "u3"), ("u2", "u3")}

    # own-best 语义：u0 的最优匹配 0.9 高于任何人 bundle 中的 0.8 → 无 envy
    assert envy["total_envy"] == 0, f"误报 envy: {envy}"
    # 独立入口与 solve_match 内嵌报告一致
    assert check_envy(pm, match_prob) == envy

    # evaluate 与 check_envy 对同一 match_prob 必须一致
    report = evaluate([["x"]], ["x"], pm, match_prob)
    assert report.envy_count_left + report.envy_count_right == envy["total_envy"]


def test_evaluate_envy_counts_all_matched_nodes():
    """b_max=1 部分匹配：u3 只匹配 u2（0.5），但 u3 对 u0 偏好 0.9 > 0.5，
    而 u0 被 u1 匹配 → u3（左侧）嫉妒 u1。

    若 match_prob 非对称存储（只存 i<j），u3 所在行为空 → 左侧 envy 被漏计。
    """
    ids = ["u0", "u1", "u2", "u3"]
    pref = [
        [0.0, 0.5, 0.0, 0.0],
        [0.5, 0.0, 0.0, 0.0],
        [0.0, 0.0, 0.0, 0.5],
        [0.9, 0.0, 0.5, 0.0],  # u3 对 u0 偏好 0.9，但 u0__u3 的 NSW=0 不匹配
    ]
    pm = _square(ids, pref)

    edges, match_prob, envy = solve_match(pm, {"b_max": 1}, _BLEND)

    pairs = {tuple(sorted((e.user1, e.user2))) for e in edges}
    assert pairs == {("u0", "u1"), ("u2", "u3")}

    # u3 嫉妒 u1（u1 拿到了 u3 更偏好的 u0）
    assert envy["left_envy_count"] == 1, f"左侧 envy 漏计: {envy}"
    report = evaluate([["x"]], ["x"], pm, match_prob)
    assert report.envy_count_left == 1


def test_match_prob_symmetric_for_same_set():
    """同集（无向）匹配的 match_prob 必须对称：prob[i,j] == prob[j,i]。"""
    ids = ["u0", "u1", "u2"]
    pref = [
        [0.0, 0.5, 0.5],
        [0.5, 0.0, 0.5],
        [0.5, 0.5, 0.0],
    ]
    pm = _square(ids, pref)
    _edges, match_prob, _envy = solve_match(pm, {"b_max": 2}, _BLEND)
    assert int(match_prob.sum()) == 6  # 完全图 3 条边 × 对称存储
    assert np.array_equal(match_prob, match_prob.T)


# ---------------------------------------------------------------------------
# 2. 同集贪心按 NSW 分数降序（全局互惠最优意图）
# ---------------------------------------------------------------------------


def test_same_set_greedy_follows_nsw_score_order():
    """b_max=1：强对 (u0,u3)=0.9、(u1,u2)=0.8 应优先于弱对 (u0,u1)=0.1。

    按索引序贪心会先取 (u0,u1)（0.1），把两个强对全部堵死 → 次优。
    """
    ids = ["u0", "u1", "u2", "u3"]
    pref = [
        [0.0, 0.1, 0.0, 0.9],
        [0.1, 0.0, 0.8, 0.0],
        [0.0, 0.8, 0.0, 0.0],
        [0.9, 0.0, 0.0, 0.0],
    ]
    pm = _square(ids, pref)

    edges, _match_prob, _envy = solve_match(pm, {"b_max": 1}, _BLEND)

    pairs = {tuple(sorted((e.user1, e.user2))) for e in edges}
    assert pairs == {("u0", "u3"), ("u1", "u2")}, f"次优匹配: {pairs}"
    total_nsw = sum(e.final_weight for e in edges)
    assert total_nsw == pytest.approx(0.9 + 0.8, abs=1e-9)


# ---------------------------------------------------------------------------
# 3. 二部图（batch）：度约束绑定 member 侧，pool 侧由 pool_b_max 控制
# ---------------------------------------------------------------------------


def _bipartite(left, right, pref_lr, pref_rl):
    return PrefMatrix(
        left_ids=list(left),
        right_ids=list(right),
        pref_left_to_right=np.asarray(pref_lr, dtype=float),
        pref_right_to_left=np.asarray(pref_rl, dtype=float),
    )


def test_bipartite_bmax_binds_member_side_only():
    """b_max=1 只绑定 member（左）侧：A、B 都能匹配热门 pool 用户 P1。

    pool_b_max=None → P1 度数不限；pool_b_max=1 → B 转而匹配 P2。
    """
    left = ["A", "B"]
    right = ["P1", "P2"]
    # pref_lr[member, pool]：A、B 都最爱 P1
    pref_lr = [
        [0.9, 0.3],
        [0.9, 0.5],
    ]
    # pref_rl[pool, member]：P1 对 A/B 均高，P2 只对 B 有分
    pref_rl = [
        [0.9, 0.9],
        [0.0, 0.5],
    ]
    pm = _bipartite(left, right, pref_lr, pref_rl)

    # pool 侧不限 → A-P1、B-P1（P1 度 2）
    edges, _mp, _envy = solve_match(pm, {"b_max": 1, "pool_b_max": None}, _BLEND)
    pairs = {tuple(sorted((e.user1, e.user2))) for e in edges}
    assert pairs == {("A", "P1"), ("B", "P1")}, f"pool 侧被 b_max 错误约束: {pairs}"

    # pool_b_max=1 → P1 只能被匹配一次，B 转匹配 P2
    edges2, _mp2, _envy2 = solve_match(pm, {"b_max": 1, "pool_b_max": 1}, _BLEND)
    pairs2 = {tuple(sorted((e.user1, e.user2))) for e in edges2}
    assert pairs2 == {("A", "P1"), ("B", "P2")}


def test_batch_mode_pool_side_unbounded(monkeypatch, fake_llm, config):
    """run_batch_match 端到端：b_max=1 时 member 侧各 1 条，热门 pool 用户
    bob 可被 alice、carol 同时匹配（pool_b_max=None 不限）。

    carol 对 bob 的 NSW（sqrt(0.82*0.83)≈0.825）高于 carol__david（≈0.477），
    故期望 {alice__bob, bob__carol}。方形同集路径会让 bob 也被 b_max=1 封顶。
    """
    from test_runners import fake_embed, fake_extract, fake_hyde, install_fake_stages

    install_fake_stages(monkeypatch)
    profiles = [
        Profile(
            id=uid,
            sections={name: f"{uid} {name}" for name in ("needs", "project", "skills", "vision")},
        )
        for uid in ("alice", "bob", "carol", "david")
    ]
    extracted = fake_extract(profiles, {}, None)
    pool_bundle = fake_embed(extracted, fake_hyde(extracted, {}, None), {})

    cfg = copy.deepcopy(config)
    cfg["budgets"]["n_profiles_to_score_together"] = 1  # fake 按单对查表打分
    cfg["matching"]["b_max"] = 1
    cfg["matching"]["pool_b_max"] = None

    batch = runners.run_batch_match(
        ["alice", "carol"],
        pool_bundle,
        cfg,
        llm_wrapper=fake_llm,
        pool_sections=extracted,
    )

    by_id = {e.pair_id: e for e in batch.match_result.edges}
    assert set(by_id) == {"alice__bob", "bob__carol"}, f"实际边: {sorted(by_id)}"

    # 方向分数随二部图方向正确落位：edge user1=member carol → bob
    edge = by_id["bob__carol"]
    assert (edge.user1, edge.user2) == ("carol", "bob")
    assert edge.llm_score_a_to_b == pytest.approx(0.82, abs=1e-6)  # carol→bob
    assert edge.llm_score_b_to_a == pytest.approx(0.83, abs=1e-6)  # bob→carol
