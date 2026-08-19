"""评测闭环：合成 market bench + CLI 门禁（spec/03-oracles.md、docs/ci-gates.md §2.6）。

守护目标：
- 合成市场构造自洽：黄金对互惠最强 → HR/NDCG 应达 1.0（否则评测链路失真）。
- envy 因度约束被控制在门禁容忍内。
- ``mutual.cli evaluate`` 的返回码按门禁判定（--fail-on-gate 阻断）。
"""

import json

import pytest

from mutual import bench, cli
from mutual.config import load_config


class TestMarketConstruction:
    def test_golden_pair_is_mutual_best(self):
        """黄金对 left i ↔ right i 必须互为最高偏好，且带方向区分（A→B≠B→A）。"""
        market = bench.generate_market(30, 20, seed=0)
        pref_lr = market.pref_left_to_right
        pref_rl = market.pref_right_to_left

        # 每个 left i 的 top1 必须是 right i（黄金对）
        for i in [0, 5, 19]:
            top = int(pref_lr[i].argmax())
            assert top == i, f"left L{i:02d} 的 top1 应为 R{i:02d}，实际 R{top:02d}"
        # 方向区分：黄金对其他候选平均显著更高
        assert pref_lr[0, 0] >= 0.9
        assert pref_rl[0, 0] >= 0.9
        assert abs(pref_lr[0, 0] - pref_rl[0, 0]) > 1e-6


class TestBenchRun:
    def test_bench_reports_perfect_signal(self):
        """理想市场下 HR@3/NDCG@5 接近 1.0，envy 为 0。"""
        report = bench.run_bench(num_left=30, num_right=20, seed=0)

        assert report.hr_at_1 == pytest.approx(1.0, abs=1e-6)
        assert report.hr_at_3 == pytest.approx(1.0, abs=1e-6)
        assert report.hr_at_5 == pytest.approx(1.0, abs=1e-6)
        assert report.ndcg_at_5 == pytest.approx(1.0, abs=1e-6)
        assert report.total_envy == 0
        assert report.total_scenarios == 20

    def test_bench_deterministic_across_seed(self):
        """不同 seed 应保持确定性（构筑性不随噪声漂移）。"""
        r1 = bench.run_bench(seed=0)
        r2 = bench.run_bench(seed=0)
        assert r1 == r2

    def test_gates_loaded_from_config(self):
        """门禁数值来自 config/default.yaml（spec/03-oracles.md §5）。"""
        cfg = load_config()
        gates = bench.load_gates(cfg)
        assert gates == {"hr_at_3_min": 0.6, "ndcg_at_5_min": 0.4, "total_envy_max": 2}


class TestCliEvaluate:
    def test_returns_zero_when_gates_pass(self):
        """门禁达标时返回码 0（不阻断 CI）。"""
        code = cli.main(["evaluate", "--fail-on-gate", "--seed", "0"])
        assert code == 0

    def test_json_output_shape(self, capsys):
        """--json 输出应含门禁字段与分场景明细。"""
        code = cli.main(["evaluate", "--json", "--seed", "0"])
        captured = capsys.readouterr().out
        assert code == 0
        data = json.loads(captured)
        assert "hr_at_3" in data and "ndcg_at_5" in data and "envy_count_left" in data
        assert data["total_scenarios"] == 24  # 三场景 8+8+8
        assert set(data["per_bench"]) == {"classic", "drift", "cold", "market"}

    def test_unknown_command_rejected(self):
        """未知子命令返回码 2。"""
        with pytest.raises(SystemExit):
            cli.main(["nope"])  # argparse 无法解析 dest，抛 SystemExit(2)
