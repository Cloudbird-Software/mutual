"""Mutual — CLI 入口（评测门禁 + bench 运行 + 反馈校准）。

用法：
    python -m mutual.cli evaluate [--config PATH] [--seed S] [--fail-on-gate] [--json]
    python -m mutual.cli calibrate --history reports.json [--embedding-only]

设计原则：全部离线、确定性，不调用真实 LLM，CI 无需 API 凭据即可运行。

评测套件（``evaluate``）：
- classic / drift / cold 三场景（强模型标注数据，推荐列表源自求解器输出）
  → 聚合 HR@1/3/5、NDCG@5 做 HR/NDCG 门禁；
- market 合成市场（构造性 oracle）→ 与三场景一起汇总 envy 做公平门禁。
"""

from __future__ import annotations

import argparse
import json
import sys
from typing import List

from . import bench, feedback
from .config import load_config
from .schemas import EvaluationReport


def _build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="mutual",
        description="Mutual 双向互惠推荐引擎 — CLI",
    )
    sub = p.add_subparsers(dest="command", required=True)

    ev = sub.add_parser("evaluate", help="运行离线评测套件并判定门禁")
    ev.add_argument("--config", default="config/default.yaml", help="配置文件路径（门禁数值来源）")
    ev.add_argument("--seed", type=int, default=0, help="合成市场/bench 随机种子")
    ev.add_argument("--noise-scale", type=float, default=0.24, help="surrogate 噪声幅度")
    ev.add_argument("--fail-on-gate", action="store_true", help="门禁未达标时非零退出（CI 阻断）")
    ev.add_argument("--json", action="store_true", help="以 JSON 输出评测报告")

    cal = sub.add_parser("calibrate", help="按评测历史做权重/prompt 校准（反馈注入）")
    cal.add_argument(
        "--history",
        required=True,
        help="评测历史 JSON 文件（list of EvaluationReport.to_dict()，时间升序）",
    )
    cal.add_argument("--embedding-only", action="store_true", help="只输出 prompt 校准块，不调权重")
    return p


def _cmd_evaluate(args: argparse.Namespace) -> int:
    config = load_config(args.config)
    reports = bench.run_suite(seed=args.seed, noise_scale=args.noise_scale)

    scenario_reports = [reports[n] for n in bench.SCENARIO_NAMES]
    quality_agg = bench.aggregate_reports(scenario_reports)
    # envy 门禁覆盖全部信号源（三场景 + market 构造性 oracle）
    total_envy = quality_agg.total_envy + reports["market"].total_envy
    gate_report = EvaluationReport(
        hr_at_1=quality_agg.hr_at_1,
        hr_at_3=quality_agg.hr_at_3,
        hr_at_5=quality_agg.hr_at_5,
        ndcg_at_5=quality_agg.ndcg_at_5,
        envy_count_left=total_envy,  # 门禁只看总和，左右分解无意义
        envy_count_right=0,
        total_scenarios=quality_agg.total_scenarios,
    )
    gates = bench.load_gates(config)

    if args.json:
        payload = gate_report.to_dict()
        payload["per_bench"] = {name: r.to_dict() for name, r in reports.items()}
        print(json.dumps(payload, ensure_ascii=False, indent=2))
    else:
        print("--- Mutual 评测报告（三场景 bench + 合成市场） ---")
        for name, r in reports.items():
            print(
                f"  {name:<8} HR@3={r.hr_at_3:.3f} NDCG@5={r.ndcg_at_5:.3f} "
                f"envy={r.total_envy} scenarios={r.total_scenarios}"
            )
        print(
            f"  门禁输入: HR@3={gate_report.hr_at_3:.3f} "
            f"NDCG@5={gate_report.ndcg_at_5:.3f} total_envy={total_envy}"
        )
        print(f"  门禁   : {gates}")
        passed = gate_report.passes_gates(gates)
        print(f"  结果   : {'PASS' if passed else 'FAIL'} ({'通过门禁' if passed else '未达门禁'})")

    if args.fail_on_gate and not gate_report.passes_gates(gates):
        return 1
    return 0


def _cmd_calibrate(args: argparse.Namespace) -> int:
    with open(args.history, "r", encoding="utf-8") as fh:
        raw = json.load(fh)
    history = [
        EvaluationReport(
            hr_at_1=float(e.get("hr_at_1", 0.0)),
            hr_at_3=float(e.get("hr_at_3", 0.0)),
            hr_at_5=float(e.get("hr_at_5", 0.0)),
            ndcg_at_5=float(e.get("ndcg_at_5", 0.0)),
            envy_count_left=int(e.get("envy_count_left", 0)),
            envy_count_right=int(e.get("envy_count_right", 0)),
            total_scenarios=int(e.get("total_scenarios", 0)),
        )
        for e in raw
    ]
    if len(history) < 2 and not args.embedding_only:
        print("history 不足两条：权重校准需要 current+previous，输出 prompt 校准块。")

    prompt_block = feedback.calibrate_prompt("Score this match...", history)
    print("=== Prompt 校准块 ===")
    print(prompt_block)

    if not args.embedding_only and len(history) >= 2:
        blending = {"embed_weight": 0.35, "llm_weight": 0.65}
        new_blending = feedback.calibrate_weights(blending, history[-1], history[-2])
        print("=== 权重校准 ===")
        print(f"  {blending} -> {new_blending}")
    return 0


def main(argv: List[str] | None = None) -> int:
    args = _build_parser().parse_args(argv)
    if args.command == "evaluate":
        return _cmd_evaluate(args)
    if args.command == "calibrate":
        return _cmd_calibrate(args)
    return 2


if __name__ == "__main__":
    sys.exit(main())
