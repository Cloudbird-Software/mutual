"""Mutual — 互惠评测 bench（离线评测 oracle 数据源）。

两层数据源（spec/03-oracles.md §3）：

1. **合成市场**（:func:`generate_market` / :func:`run_bench`）：黄金对构造
   性 oracle，守护 envy 门禁与评测链路自洽（HR 构造性 = 1.0）。
2. **三场景 bench**（:func:`run_scenario` / :func:`run_scenarios`，
   data/bench/*.json）：强模型标注的真实语义画像 + 黄金真值对——
   - classic：经典互惠（稳定双向偏好 + 资源竞争者）
   - drift：兴趣演化（t2 偏好漂移，真值随之改变）
   - cold：冷启动（新实体无打分历史，仅 embedding 信号）

三场景的**推荐列表来自求解器输出**（匹配边按权重排序），因此求解器或
打分链路的退化会直接传导到 HR/NDCG —— 与合成市场不同（后者 HR 只验证
数据构造）。信号源用 :mod:`mutual.surrogate`（确定性 LLM/embedder 替身，
带固定 seed 噪声模拟判断不完美），CI 无需真实凭据即可复现。

评测闭环（被 :func:`mutual.cli` 的 ``evaluate`` 子命令调用）：
    1. 加载场景数据 → surrogate 打分 → 生产路径 pre_matrix → PrefMatrix。
    2. ``solve_match`` 求解（b_max/pool_b_max 度约束）。
    3. 每 member 的推荐 = 其匹配边按 final_weight 降序（求解器实际输出）。
    4. 与黄金真值比对 → HR/NDCG/envy（:func:`mutual.evaluate.evaluate`）。
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Dict, List

import numpy as np

from . import surrogate
from .evaluate import evaluate
from .match import solve_match
from .schemas import EvaluationReport, PrefMatrix

# 三场景 bench 数据目录（强模型标注的评测集，spec/03-oracles.md §3.2）
BENCH_DATA_DIR = Path(__file__).resolve().parent.parent.parent / "data" / "bench"
SCENARIO_NAMES = ("classic", "drift", "cold")
# 场景固定 seed 偏移（同一 seed 下各场景噪声独立且跨版本稳定）
_SCENARIO_SEED_OFFSET = {"classic": 0, "drift": 101, "cold": 202}


def generate_market(num_left: int, num_right: int, seed: int = 0) -> PrefMatrix:
    """生成确定性双边互惠偏好市场。

    构造规则（使存在清晰互惠最优解，作为可验证 oracle）：
    - 黄金对：``left i ↔ right i``（``i < min(num_left, num_right)``）——
      双向偏好 ``0.9 + 0.09*((i + seed) % 5)``（带轻微序列差异，保证 A→B
      与 B→A 方向可区分），使黄金对互为最高偏好。
    - 非黄金对：偏好 ``0.2``（低噪声），位于黄金偏好之下。
      ``left`` 与 ``right`` 中的多余数量（``num_left != num_right`` 时）
      仅贡献低偏好噪声候选，无黄金真值 → 验证系统不被噪声干扰。

    Args:
        num_left: 左（member）侧实体数。
        num_right: 右（pool）侧实体数。
        seed: 固定随机种子；改变它可产生不同噪声布局（默认 0 用于 CI）。

    Returns:
        :class:`~mutual.schemas.PrefMatrix`（M×N 矩形市场）。
    """
    rng = np.random.RandomState(seed)
    ids_left = [f"L{i:02d}" for i in range(num_left)]
    ids_right = [f"R{i:02d}" for i in range(num_right)]

    pref_lr = np.full((num_left, num_right), 0.2, dtype=float)
    pref_rl = np.full((num_right, num_left), 0.2, dtype=float)

    n_gold = min(num_left, num_right)
    for i in range(n_gold):
        gold = 0.9 + 0.09 * ((i + seed) % 5)
        # 双向都为黄金对加权；方向各带独立轻微扰动，保证 A→B ≠ B→A。
        lr, rl = gold, gold + 0.02 * rng.rand()
        pref_lr[i, i] = lr
        pref_rl[i, i] = rl

    # 低噪声随机扰动，模拟真实市场的不完美信息。
    pref_lr += 0.05 * rng.rand(num_left, num_right)
    pref_rl += 0.05 * rng.rand(num_right, num_left)
    pref_lr = np.clip(pref_lr, 0.0, 1.0)
    pref_rl = np.clip(pref_rl, 0.0, 1.0)

    return PrefMatrix(
        left_ids=ids_left,
        right_ids=ids_right,
        pref_left_to_right=pref_lr,
        pref_right_to_left=pref_rl,
    )


def golden_truth(market: PrefMatrix) -> Dict[str, str]:
    """黄金真值标记：``left i ↔ right i``（i < min(M, N)）。"""
    ids_left = market.left_ids
    ids_right = market.right_ids
    n_gold = min(len(ids_left), len(ids_right))
    return {ids_left[i]: ids_right[i] for i in range(n_gold)}


def run_bench(
    num_left: int = 30,
    num_right: int = 20,
    seed: int = 0,
    b_max: int = 1,
    pool_b_max: int | None = 1,
) -> EvaluationReport:
    """跑一轮离线互惠评测，返回 :class:`~mutual.schemas.EvaluationReport`。

    Steps:
        1. 生成合成市场。
        2. ``solve_match`` 依偏好求解（受 ``b_max``/``pool_b_max`` 约束，
           非黄金对因噪声无法高于黄金对 → 不会被选出）。
        3. 推荐列表：每个左节点按 ``pref_left_to_right`` 降序取 top-5
           （猜测推荐器输出），真值 = 黄金对右节点。
        4. ``evaluate`` 计算 HR@1/3/5、NDCG@5 与 envy，构造报告。

    Args:
        num_left: 左侧实体数（默认 30）。
        num_right: 右侧实体数（默认 20）。
        seed: 市场随机种子。
        b_max: 左（member）侧度上限。
        pool_b_max: 右（pool）侧度上限；``None`` 表示不限（一配多）。

    Returns:
        :class:`~mutual.schemas.EvaluationReport`。

    Raises:
        AssertionError: 若求解未能命中全部黄金对（HR/NDCG 未达标）——
            说明匹配器或评测链路存在回归，CI 门禁会据此阻断。
    """
    market = generate_market(num_left, num_right, seed)
    truth = golden_truth(market)

    edges, match_prob, _envy_report = solve_match(
        market,
        matching_config={"b_max": b_max, "pool_b_max": pool_b_max},
        blending_config={"embed_weight": 0.5, "llm_weight": 0.5},
    )

    # 推荐列表 = **求解器实际输出**（qodo #2）：每个左节点的匹配边按
    # final_weight 降序（与 run_scenario 同构），黄金对应排首位。
    # 求解器退化（丢边/错边）会直接压低 HR/NDCG——CI oracle 不再被
    # 偏好矩阵重排"美化"。
    by_left: Dict[str, List[tuple[float, str]]] = {lid: [] for lid in market.left_ids}
    for e in edges:
        if e.user1 in by_left:
            by_left[e.user1].append((e.final_weight, e.user2))
        elif e.user2 in by_left:
            by_left[e.user2].append((e.final_weight, e.user1))
    top_k = 5
    predictions: List[List[str]] = []
    ground_truth: List[str] = []

    for lid in market.left_ids:
        if lid not in truth:
            continue  # 无真值：不算作评测场景
        ranked = [pid for _w, pid in sorted(by_left[lid], reverse=True)]
        predictions.append(ranked[:top_k])
        ground_truth.append(truth[lid])

    report = evaluate(predictions, ground_truth, market, match_prob)

    # 构筑性校验：黄金对必须是每个场景的第一推荐（HR@3 → 1.0）。
    # 若合成数据构造有误，评测链路本身就会失真，这里在进入门禁判定前阻断。
    assert report.hr_at_3 >= 0.99, f"bench 构筑性失败: HR@3={report.hr_at_3:.3f}"
    return report


def load_gates(config: Dict) -> Dict[str, float]:
    """从配置读取评测门禁数值（spec/03-oracles.md §5、config/default.yaml）。"""
    ev = config.get("evaluation") or {}
    return dict(ev.get("gates") or {})


# ---------------------------------------------------------------------------
# 三场景 bench（强模型标注数据 + 求解器输出驱动的推荐列表）
# ---------------------------------------------------------------------------


def load_scenario(name: str, data_dir: Path | None = None) -> Dict:
    """加载场景数据（data/bench/{name}.json）。"""
    if name not in SCENARIO_NAMES:
        raise ValueError(f"未知场景 {name!r}，可选: {SCENARIO_NAMES}")
    base = data_dir if data_dir is not None else BENCH_DATA_DIR
    with open(base / f"{name}.json", "r", encoding="utf-8") as fh:
        return json.load(fh)


def run_scenario(
    name: str,
    seed: int = 0,
    noise_scale: float = 0.24,
    b_max: int = 3,
    pool_b_max: int | None = 1,
    data_dir: Path | None = None,
) -> EvaluationReport:
    """跑单个场景：surrogate 打分 → pre_matrix → solve_match → 评测。

    推荐列表 = 该 member 的匹配边按 ``final_weight`` 降序（**求解器实际
    输出**，非偏好矩阵重排）→ 求解器退化直接压低 HR/NDCG。

    Args:
        name: 场景名（classic / drift / cold）。
        seed: 全局种子（加场景固定偏移后驱动 surrogate 噪声）。
        noise_scale: 噪声幅度（0 = 完美信号，仅调试用）。
        b_max: member 侧度上限（同时是推荐列表长度上限）。
        pool_b_max: pool 侧度上限。
        data_dir: 数据目录覆盖（测试注入用）。

    Returns:
        :class:`~mutual.schemas.EvaluationReport`。
    """
    data = load_scenario(name, data_dir)
    members: Dict[str, Dict[str, str]] = data["members"]
    pool: Dict[str, Dict[str, str]] = data["pool"]
    truth: Dict[str, str] = data["ground_truth"]
    embedding_only = bool(data.get("embedding_only", False))

    sseed = seed + _SCENARIO_SEED_OFFSET[name]
    scores = surrogate.score_matrix(
        members, pool, seed=sseed, noise_scale=noise_scale, embedding_only=embedding_only
    )

    member_ids = list(members)
    pool_ids = list(pool)
    m_idx = {mid: i for i, mid in enumerate(member_ids)}
    p_idx = {pid: j for j, pid in enumerate(pool_ids)}

    pref_lr = np.zeros((len(member_ids), len(pool_ids)), dtype=float)
    pref_rl = np.zeros((len(pool_ids), len(member_ids)), dtype=float)
    for mid, row in scores.items():
        for pid, (a2b, b2a) in row.items():
            i, j = m_idx[mid], p_idx[pid]
            pref_lr[i, j] = a2b
            pref_rl[j, i] = b2a

    pref_matrix = PrefMatrix(
        left_ids=member_ids,
        right_ids=pool_ids,
        pref_left_to_right=pref_lr,
        pref_right_to_left=pref_rl,
    )
    edges, match_prob, _envy = solve_match(
        pref_matrix,
        matching_config={"b_max": b_max, "pool_b_max": pool_b_max},
        blending_config={"embed_weight": 0.5, "llm_weight": 0.5},
    )

    # 推荐列表：member 的匹配边按 final_weight 降序（求解器输出）。
    by_member: Dict[str, List[tuple[float, str]]] = {mid: [] for mid in member_ids}
    for e in edges:
        if e.user1 in by_member:
            by_member[e.user1].append((e.final_weight, e.user2))
        elif e.user2 in by_member:
            by_member[e.user2].append((e.final_weight, e.user1))
    predictions: List[List[str]] = []
    ground_truth: List[str] = []
    for mid in member_ids:
        if mid not in truth:
            continue  # 竞争者无真值，不计评测场景
        ranked = [pid for _w, pid in sorted(by_member[mid], reverse=True)]
        predictions.append(ranked[: max(b_max, 1)])
        ground_truth.append(truth[mid])

    return evaluate(predictions, ground_truth, pref_matrix, match_prob)


def run_scenarios(
    seed: int = 0,
    noise_scale: float = 0.24,
    data_dir: Path | None = None,
) -> Dict[str, EvaluationReport]:
    """跑全部三场景，返回 ``{场景名 → EvaluationReport}``。

    默认 ``noise_scale=0.24``：经数值标定，聚合 HR@3≈0.96、envy=1 ——
    门禁（0.6/0.4/2）有余量通过，且 classic 场景存在真实判别度
    （HR@3≈0.88 < 1.0，求解器/打分退化会被传导）。
    """
    return {
        name: run_scenario(name, seed=seed, noise_scale=noise_scale, data_dir=data_dir)
        for name in SCENARIO_NAMES
    }


def aggregate_reports(reports: List[EvaluationReport]) -> EvaluationReport:
    """按场景数加权聚合多份报告（HR/NDCG 加权平均，envy 求和）。"""
    total = sum(r.total_scenarios for r in reports)
    if total == 0:
        return EvaluationReport(
            hr_at_1=0.0,
            hr_at_3=0.0,
            hr_at_5=0.0,
            ndcg_at_5=0.0,
            envy_count_left=0,
            envy_count_right=0,
            total_scenarios=0,
        )

    def _wavg(attr: str) -> float:
        return sum(getattr(r, attr) * r.total_scenarios for r in reports) / total

    return EvaluationReport(
        hr_at_1=_wavg("hr_at_1"),
        hr_at_3=_wavg("hr_at_3"),
        hr_at_5=_wavg("hr_at_5"),
        ndcg_at_5=_wavg("ndcg_at_5"),
        envy_count_left=sum(r.envy_count_left for r in reports),
        envy_count_right=sum(r.envy_count_right for r in reports),
        total_scenarios=total,
        metadata={
            "per_scenario": {
                r.metadata.get("scenario", i): r.to_dict() for i, r in enumerate(reports)
            }
        },
    )


def run_suite(seed: int = 0, noise_scale: float = 0.24) -> Dict[str, EvaluationReport]:
    """完整评测套件：三场景 + 合成市场（market 只贡献 envy 门禁信号）。

    Returns:
        ``{classic, drift, cold, market} → EvaluationReport``；
        CLI 以三场景聚合做 HR/NDCG 门禁、全部四项做 envy 门禁。
    """
    reports = run_scenarios(seed=seed, noise_scale=noise_scale)
    market_report = run_bench(seed=seed)
    market_report.metadata["scenario"] = "market"
    out: Dict[str, EvaluationReport] = {}
    for name, r in reports.items():
        r.metadata["scenario"] = name
        out[name] = r
    out["market"] = market_report
    return out
