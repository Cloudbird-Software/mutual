"""Phase 3 评测闭环测试：三场景 bench + surrogate + 反馈注入。

最关键的守护（本文件存在的理由）：
- ``test_gate_discriminates_broken_solver``：把求解器换成乱配后，门禁必须
  失败 —— 修复「HR/NDCG 门禁无判别力」缺陷的回归证明。
"""

import json

import numpy as np
import pytest

from mutual import bench, cli, feedback, surrogate
from mutual.schemas import EvaluationReport

_GATES = {"hr_at_3_min": 0.6, "ndcg_at_5_min": 0.4, "total_envy_max": 2}


# ---------------------------------------------------------------------------
# Surrogate 信号源
# ---------------------------------------------------------------------------


class TestSurrogate:
    def test_tokenize(self):
        assert surrogate.tokenize("Rust Blockchain, consensus!") == [
            "rust",
            "blockchain",
            "consensus",
        ]

    def test_directional_score_needs_skills_signal(self):
        """A 的 needs 与 B 的 skills 重叠 → 高分；不重叠 → 低分。"""
        a = {"needs": "rust blockchain", "project": "", "skills": "", "vision": ""}
        b_match = {"needs": "", "project": "", "skills": "rust blockchain", "vision": ""}
        b_other = {"needs": "", "project": "", "skills": "choir arranging bilingual", "vision": ""}
        assert surrogate.directional_score(a, b_match) == pytest.approx(0.6)  # 完全重叠 × 主权重
        assert surrogate.directional_score(a, b_other) == pytest.approx(0.0)
        # 部分重叠 → 中间值（单调性）
        b_partial = {"needs": "", "project": "", "skills": "rust performance tuning", "vision": ""}
        s = surrogate.directional_score(a, b_partial)
        assert 0.0 < s < 0.6

    def test_directional_score_semantics(self):
        """语义规则：A 的 needs 对 B 的 skills（而非 skills 对 skills）。"""
        a = {"needs": "rust", "project": "", "skills": "choir", "vision": ""}
        b = {"needs": "choir", "project": "", "skills": "rust", "vision": ""}
        assert surrogate.directional_score(a, b) == pytest.approx(0.6)
        assert surrogate.directional_score(b, a) == pytest.approx(0.6)
        # skills 对 skills 不计分：C 的 skills 与 A 的 skills 同域但非 A 所需
        c = {"needs": "", "project": "", "skills": "choir conducting", "vision": ""}
        assert surrogate.directional_score(a, c) == pytest.approx(0.0)

    def test_embed_score_symmetric_and_bounded(self):
        a = {"needs": "bee hives sensors", "project": "", "skills": "", "vision": ""}
        b = {"needs": "", "project": "", "skills": "beekeeping urban hives", "vision": ""}
        s1 = surrogate.embed_score(a, b)
        assert s1 == pytest.approx(surrogate.embed_score(b, a))
        assert 0.0 <= s1 <= 1.0

    def test_score_matrix_deterministic(self):
        members = {"m": {"needs": "rust", "project": "", "skills": "", "vision": ""}}
        pool = {"p": {"needs": "", "project": "", "skills": "rust", "vision": ""}}
        s1 = surrogate.score_matrix(members, pool, seed=7, noise_scale=0.3)
        s2 = surrogate.score_matrix(members, pool, seed=7, noise_scale=0.3)
        assert s1 == s2


# ---------------------------------------------------------------------------
# 三场景 bench
# ---------------------------------------------------------------------------


class TestScenarios:
    def test_all_scenarios_run_and_are_pinned(self):
        """三场景确定性回归锚点（数值变化 = 求解器/打分链路行为变化）。"""
        reports = bench.run_scenarios()
        assert reports["classic"].hr_at_3 == pytest.approx(0.875)
        assert reports["classic"].ndcg_at_5 == pytest.approx(0.875)
        assert reports["drift"].hr_at_3 == pytest.approx(1.0)
        assert reports["cold"].hr_at_3 == pytest.approx(1.0)
        agg = bench.aggregate_reports(list(reports.values()))
        assert agg.hr_at_3 == pytest.approx((0.875 + 1.0 + 1.0) / 3)
        assert agg.total_envy == 1  # 门禁余量（上限 2）
        assert agg.passes_gates(_GATES)

    def test_determinism_same_seed(self):
        r1 = bench.run_scenarios(seed=5)
        r2 = bench.run_scenarios(seed=5)
        for name in bench.SCENARIO_NAMES:
            assert r1[name].to_dict() == r2[name].to_dict()

    def test_zero_noise_perfect_signal(self):
        """noise=0：surrogate = 完美信号，黄金对应全部命中（数据自洽性）。"""
        reports = bench.run_scenarios(noise_scale=0.0)
        for name, r in reports.items():
            assert r.hr_at_3 == pytest.approx(1.0), f"{name} 在零噪声下未全命中"

    def test_drift_truth_actually_drifted(self):
        """drift 场景标注自洽：t2 真值相对 t1 发生了漂移（m0/m1 互换）。"""
        data = bench.load_scenario("drift")
        t1, t2 = data["ground_truth_t1"], data["ground_truth"]
        assert t1 != t2
        assert t2["dm0"] == t1["dm1"] and t2["dm1"] == t1["dm0"]

    def test_cold_uses_embedding_only(self):
        data = bench.load_scenario("cold")
        assert data["embedding_only"] is True

    def test_unknown_scenario_rejected(self):
        with pytest.raises(ValueError, match="未知场景"):
            bench.load_scenario("nope")


# ---------------------------------------------------------------------------
# 门禁判别力（核心回归证明）
# ---------------------------------------------------------------------------


class TestGateDiscrimination:
    def test_gate_discriminates_broken_solver(self, monkeypatch):
        """坏求解器（乱配）必须导致门禁失败。

        背景：旧 market bench 的 HR/NDCG 不经过求解器，乱配求解器下
        HR@3 仍为 1.0（已实测证明）。三场景 bench 的推荐列表源自求解器
        输出，因此乱配 → HR 崩塌 → 门禁 FAIL。此测试守护该性质。
        """
        good = bench.run_scenarios()
        assert bench.aggregate_reports(list(good.values())).passes_gates(_GATES)

        def broken_solver(pref_matrix, matching_config, blending_config, reference_scores=None):
            m, n = len(pref_matrix.left_ids), len(pref_matrix.right_ids)
            rng = np.random.RandomState(42)
            mp = np.zeros((m, n), dtype=int)
            for i in range(m):  # 完全无视偏好的乱配
                mp[i, rng.randint(n)] = 1
            return [], mp, {"total_envy": 999}

        monkeypatch.setattr(bench, "solve_match", broken_solver)
        bad = bench.run_scenarios()
        agg = bench.aggregate_reports(list(bad.values()))
        assert agg.hr_at_3 < _GATES["hr_at_3_min"], "乱配求解器未压低 HR —— 门禁无判别力！"
        assert not agg.passes_gates(_GATES)

    def test_gate_discriminates_wrong_ground_truth(self, tmp_path):
        """错误真值（黄金对整体右移一位）必须导致门禁失败（标注有效性）。"""
        data = bench.load_scenario("classic")
        truth = data["ground_truth"]
        pool_ids = list(data["pool"])
        data["ground_truth"] = {
            m: pool_ids[(pool_ids.index(p) + 1) % len(pool_ids)] for m, p in truth.items()
        }
        d = tmp_path / "bench"
        d.mkdir()
        (d / "classic.json").write_text(json.dumps(data, ensure_ascii=False), encoding="utf-8")

        report = bench.run_scenario("classic", data_dir=d)
        assert report.hr_at_3 < _GATES["hr_at_3_min"]


# ---------------------------------------------------------------------------
# 聚合与套件
# ---------------------------------------------------------------------------


class TestAggregate:
    def test_weighted_average(self):
        r1 = EvaluationReport(hr_at_1=1, hr_at_3=1, hr_at_5=1, ndcg_at_5=1, total_scenarios=8)
        r2 = EvaluationReport(
            hr_at_1=0, hr_at_3=0, hr_at_5=0, ndcg_at_5=0, envy_count_left=3, total_scenarios=2
        )
        agg = bench.aggregate_reports([r1, r2])
        assert agg.hr_at_3 == pytest.approx(0.8)  # 8/10
        assert agg.total_envy == 3
        assert agg.total_scenarios == 10

    def test_empty(self):
        agg = bench.aggregate_reports([])
        assert agg.total_scenarios == 0 and agg.hr_at_3 == 0.0

    def test_suite_shape(self):
        reports = bench.run_suite()
        assert set(reports) == {"classic", "drift", "cold", "market"}
        assert reports["market"].total_scenarios == 20


# ---------------------------------------------------------------------------
# 反馈注入（Phase 3 §5.2）
# ---------------------------------------------------------------------------


def _report(hr3: float, envy: int = 0) -> EvaluationReport:
    return EvaluationReport(
        hr_at_1=hr3,
        hr_at_3=hr3,
        hr_at_5=hr3,
        ndcg_at_5=hr3,
        envy_count_left=envy,
        total_scenarios=8,
    )


class TestFeedback:
    def test_prompt_calibration_injects_history(self):
        out = feedback.calibrate_prompt("BASE PROMPT", [_report(0.9), _report(0.7)])
        assert out.startswith("[Calibration]")
        assert "BASE PROMPT" in out
        assert "HR@3=0.90" in out and "HR@3=0.70" in out

    def test_prompt_calibration_empty_history_noop(self):
        assert feedback.calibrate_prompt("X", []) == "X"

    def test_prompt_calibration_max_entries(self):
        out = feedback.calibrate_prompt("X", [_report(0.5) for _ in range(10)], max_entries=2)
        assert out.count("HR@3=") == 2

    def test_weight_calibration_triggers_on_drop(self):
        blending = {"embed_weight": 0.35, "llm_weight": 0.65}
        out = feedback.calibrate_weights(blending, _report(0.5), _report(0.9))
        assert out["llm_weight"] > blending["llm_weight"]
        assert out["embed_weight"] < blending["embed_weight"]
        assert out["embed_weight"] + out["llm_weight"] == pytest.approx(1.0)
        assert blending == {"embed_weight": 0.35, "llm_weight": 0.65}  # 不改入参

    def test_weight_calibration_noop_without_drop(self):
        blending = {"embed_weight": 0.35, "llm_weight": 0.65}
        assert feedback.calibrate_weights(blending, _report(0.9), _report(0.5)) == blending
        assert feedback.calibrate_weights(blending, _report(0.9), None) == blending

    def test_weight_calibration_bounded(self):
        blending = {"embed_weight": 0.89, "llm_weight": 0.11}
        out = feedback.calibrate_weights(blending, _report(0.1), _report(0.9), step=0.9)
        assert out["embed_weight"] >= 0.1 - 1e-9 and out["llm_weight"] >= 0.1 - 1e-9

    def test_match_memory_roundtrip(self, tmp_path):
        mem = feedback.MatchMemory()
        mem.record("a__b", True, "great fit")
        mem.record("a__c", False, "mismatched needs")
        assert mem.rejected_pair_ids == ["a__c"]
        assert "a__c: REJECTED — mismatched needs" in mem.prompt_block()

        path = tmp_path / "mem.jsonl"
        mem.save(path)
        loaded = feedback.MatchMemory.load(path)
        assert loaded.entries == mem.entries
        assert feedback.MatchMemory.load(tmp_path / "missing.jsonl").entries == []

    def test_match_memory_empty_block(self):
        assert feedback.MatchMemory().prompt_block() == ""


# ---------------------------------------------------------------------------
# CLI
# ---------------------------------------------------------------------------


class TestCli:
    def test_evaluate_passes_gates(self):
        assert cli.main(["evaluate", "--fail-on-gate"]) == 0

    def test_calibrate_outputs(self, tmp_path, capsys):
        hist = tmp_path / "hist.json"
        hist.write_text(
            json.dumps([_report(0.9).to_dict(), _report(0.6).to_dict()]), encoding="utf-8"
        )
        assert cli.main(["calibrate", "--history", str(hist)]) == 0
        out = capsys.readouterr().out
        assert "[Calibration]" in out
        assert "0.35" in out and "0.65" in out  # 权重校准输出
