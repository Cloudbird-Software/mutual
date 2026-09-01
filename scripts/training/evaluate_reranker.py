#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
mutual P0 Score 打分器评测脚本
==============================
在 test.jsonl + holdout 陷阱上评测训练好的 cross-encoder，输出门禁指标。
指标（对齐 TRAINING_SPEC §6.1）：
  - 黄金对 vs 非黄金对分数分离（AUC）
  - HR@1/3/5 / NDCG@5（对每个 (scenario, a) 按分数排序，黄金 b 是否进入 top-K）
  - holdout 陷阱断言逐条检查（见 check_holdout 的职责边界说明）

用法：
  python evaluate_reranker.py --model ./output/model --data ./output/data
  # zero-shot 基线：
  python evaluate_reranker.py --model BAAI/bge-reranker-v2-m3 --data ./output/data \
      --out ./output/data/baseline.json
"""
import argparse
import json
from pathlib import Path
import numpy as np
from sentence_transformers import CrossEncoder

# ---------------------------------------------------------------------------
# 校准锚点：分数 → level 0-4（TRAINING_SPEC §1.1 校准锚点）
#   0-0.1 → 0 | 0.15-0.3 → 1 | 0.35-0.55 → 2 | 0.6-0.8 → 3 | 0.85-1.0 → 4
# 边界取中点：<=0.10→0, <=0.30→1, <=0.55→2, <=0.80→3, else→4
# ---------------------------------------------------------------------------
LEVEL_BOUNDS = [0.10, 0.30, 0.55, 0.80]  # 每档上限（level 0..3 的上界；level 4 无上界）

def score_to_level(score: float) -> int:
    for lv, ub in enumerate(LEVEL_BOUNDS):
        if score <= ub:
            return lv
    return 4


def score_pairs(model, pairs, batch_size=32):
    """输入 [(a_text, b_text), ...] → 分数列表（CrossEncoder num_labels=1 输出 0-1）。"""
    if not pairs:
        return []
    scores = model.predict(pairs, batch_size=batch_size, show_progress_bar=False)
    return [float(s) for s in scores]


def rank_metrics(rows, k_values=(1, 3, 5)):
    """按 (scenario, a_id) 分组：黄金对（label=1）应排在非黄金对之前。
    注意：不同场景的 member id 可能重复（如 classic 与 constraints 都有 m1，
    decoy/messy/paraphrase 都有 m01），必须按 (scenario, a_id) 分组，否则指标被污染。
    返回 HR@K 与 NDCG@5（当前数据每 member 单黄金，按单黄金假设计算 NDCG）。
    """
    by_a = {}
    for r in rows:
        key = (r.get("scenario", "?"), r["a_id"])
        by_a.setdefault(key, []).append(r)
    hr = {k: [] for k in k_values}
    ndcg5 = []
    for key, lst in by_a.items():
        lst.sort(key=lambda x: x["score"], reverse=True)
        gold_positions = [i for i, r in enumerate(lst) if r["label"] >= 0.5]
        if not gold_positions:
            continue
        for k in k_values:
            hr[k].append(1.0 if min(gold_positions) < k else 0.0)
        # NDCG@5：单黄金假设（gold_positions 通常 1 个），取首个黄金位置
        gpos = gold_positions[0]
        dcg = 1.0 / np.log2(gpos + 2) if gpos < 5 else 0.0
        idcg = 1.0
        ndcg5.append(dcg / idcg)
    out = {}
    for k in k_values:
        out[f"HR@{k}"] = round(float(np.mean(hr[k])), 4) if hr[k] else None
    out["NDCG@5"] = round(float(np.mean(ndcg5)), 4) if ndcg5 else None
    return out


# ---------------------------------------------------------------------------
# holdout 断言职责边界（重要）：
#   holdout 8 种断言中，只有 level_le / level_ge 属于打分器职责（打分→level 判定）。
#   其余 6 种依赖全链路 pipeline，打分器侧无法单独验证，统一标记 deferred：
#     - eligible        ：硬约束检测 + 资格位（extract→eligibility 层）
#     - reason_contains ：reason 输出（LLM/pipeline 层）
#     - not_matched     ：最终匹配求解结果（pipeline 过滤→求解层）
#     - matched         ：最终匹配求解结果（pipeline 求解层）
#     - confidence_le   ：置信度维度（v3 契约额外输出，打分器无此维度）
#     - degree_le       ：图谱容量约束（求解层）
#   本脚本只对 level_le / level_ge 断言做真实判定；其余在报告中如实记录为
#   "pipeline-deferred"，由接入引擎后的全链路 holdout gate 验证（门禁分层见
#   TRAINING_SPEC §6.1b）。避免"假装 12 陷阱全绿"的虚假通过。
# ---------------------------------------------------------------------------
SCORER_ASSERTION_TYPES = {"level_le", "level_ge"}
PIPELINE_ASSERTION_TYPES = {
    "eligible", "reason_contains", "not_matched", "matched", "confidence_le", "degree_le",
}


def check_holdout(model, holdout_path, tol=0.05):
    """holdout 陷阱逐条检查。
    对每个 trap：
      - 双向打分 s_ab = score(A,B)（focal=A 时用），s_ba = score(B,A)（focal=B 时用）
      - level_le / level_ge：分数→level，与 max/min 比较（含容差 tol，抵消量化边界抖动）
      - 其余断言：标记 pipeline-deferred，不计入 pass/fail
    返回 (results, scorer_passed, scorer_total, pipeline_deferred, items_total)
    """
    with open(holdout_path, encoding="utf-8") as f:
        items = [json.loads(line) for line in f]
    results = []
    scorer_passed = 0
    scorer_total = 0
    pipeline_deferred = 0
    for it in items:
        aid, bid = it["profiles"]["A"], it["profiles"]["B"]
        s_ab = score_pairs(model, [(aid, bid)])[0]
        s_ba = score_pairs(model, [(bid, aid)])[0]
        dir_score = {"A": s_ab, "B": s_ba}
        ok = True
        detail = []
        has_scorer_assert = False
        for a in it.get("assertions", []):
            atype = a.get("type")
            aidx = a.get("id", atype)
            if atype in SCORER_ASSERTION_TYPES:
                has_scorer_assert = True
                scorer_total += 1
                focal = a.get("focal", "A")
                sc = dir_score.get(focal, s_ab)
                lv = score_to_level(sc)
                if atype == "level_le":
                    passed = lv <= a["max"]
                    detail.append(f"{aidx}(level_le): lv={lv} max={a['max']} sc={sc:.3f} {'PASS' if passed else 'FAIL'}")
                else:  # level_ge
                    passed = lv >= a["min"]
                    detail.append(f"{aidx}(level_ge): lv={lv} min={a['min']} sc={sc:.3f} {'PASS' if passed else 'FAIL'}")
                if passed:
                    scorer_passed += 1
                else:
                    ok = False
            elif atype in PIPELINE_ASSERTION_TYPES:
                pipeline_deferred += 1
                detail.append(f"{aidx}({atype}): pipeline-deferred（需全链路 gate 验证）")
            else:
                detail.append(f"{aidx}({atype}): 未知断言类型，跳过")
        results.append({
            "id": it["id"], "title": it["title"],
            "scores": {"A_to_B": round(s_ab, 3), "B_to_A": round(s_ba, 3)},
            "scorer_ok": ok if has_scorer_assert else None,
            "detail": detail,
        })
    return results, scorer_passed, scorer_total, pipeline_deferred, len(items)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--model", required=True)
    ap.add_argument("--data", required=True, help="prepare_data.py 的 --out-dir")
    ap.add_argument("--out", default=None, help="评测报告输出路径")
    args = ap.parse_args()
    data = Path(args.data)
    model = CrossEncoder(args.model, num_labels=1, max_length=512)

    # 1) test.jsonl 指标
    metrics = {}
    test_path = data / "test.jsonl"
    if test_path.exists():
        with open(test_path, encoding="utf-8") as f:
            test_rows = [json.loads(line) for line in f]
        if test_rows:
            pairs = [(r["a"], r["b"]) for r in test_rows]
            scores = score_pairs(model, pairs)
            for r, s in zip(test_rows, scores):
                r["score"] = s
            metrics = rank_metrics(test_rows)
            # AUC（Mann-Whitney：随机正对 > 随机负对的概率）
            y = np.array([r["label"] for r in test_rows])
            s = np.array([r["score"] for r in test_rows])
            n_pos = (y == 1).sum()
            n_neg = (y == 0).sum()
            if n_pos > 0 and n_neg > 0:
                pos = s[y == 1]
                neg = s[y == 0]
                auc = (pos[:, None] > neg[None, :]).mean()
                metrics["AUC"] = round(float(auc), 4)
            print(f"[evaluate] test.jsonl: {len(test_rows)} 对")
            for k, v in metrics.items():
                print(f"  {k}: {v}")
        else:
            print("[evaluate] test.jsonl 为空，跳过")
    else:
        print("[evaluate] 无 test.jsonl")

    # 2) holdout 陷阱
    holdout_path = data / "holdout.jsonl"
    holdout_res = []
    scorer_passed = scorer_total = pipeline_deferred = items_total = 0
    if holdout_path.exists():
        (holdout_res, scorer_passed, scorer_total,
         pipeline_deferred, items_total) = check_holdout(model, holdout_path)
        print(f"[evaluate] holdout 陷阱: 打分器断言 {scorer_passed}/{scorer_total} 通过"
              f"（pipeline-deferred {pipeline_deferred} 条需全链路 gate）")

    report = {
        "model": args.model,
        "metrics": metrics,
        "holdout": {
            "scorer_passed": scorer_passed,
            "scorer_total": scorer_total,
            "pipeline_deferred": pipeline_deferred,
            "items_total": items_total,
            "results": holdout_res,
        },
    }
    if args.out:
        Path(args.out).write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
        print(f"[evaluate] 报告已写入 {args.out}")
    else:
        print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
