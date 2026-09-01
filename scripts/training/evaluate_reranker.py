#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
mutual P0 Score 打分器评测脚本
==============================
在 test.jsonl + holdout 陷阱上评测训练好的 cross-encoder，输出门禁指标。

指标（对齐 TRAINING_SPEC §6.1）：
  - 黄金对 vs 非黄金对分数分离（AUC）
  - HR@1/3/5 / NDCG@5（对每个 a 按分数排序，黄金 b 是否进入 top-K）
  - holdout 陷阱断言逐条检查（level 断言 → 转换为分数区间判定）
  - 注入/堆砌对抗：陷阱内注入画像不得获高分

用法：
  python evaluate_reranker.py --model ./output/model --data ./output/data
  # zero-shot 基线：
  python evaluate_reranker.py --model BAAI/bge-reranker-v2-m3 --data ./output/data \
      --out ./output/data/baseline.json
"""
import argparse
import json
import sys
from pathlib import Path

import numpy as np
from sentence_transformers import CrossEncoder


def score_pairs(model, pairs, batch_size=32):
    """输入 [(a_text, b_text), ...] → 分数列表（CrossEncoder 输出 logit）。"""
    if not pairs:
        return []
    # CrossEncoder.predict 接受 list of (text1, text2)
    scores = model.predict(pairs, batch_size=batch_size, show_progress_bar=False)
    return [float(s) for s in scores]


def rank_metrics(rows, k_values=(1, 3, 5)):
    """按 a 分组：黄金对（label=1）应排在非黄金对之前。返回 HR@K 与 NDCG@5。"""
    by_a = {}
    for r in rows:
        by_a.setdefault(r["a_id"], []).append(r)
    hr = {k: [] for k in k_values}
    ndcg5 = []
    for a_id, lst in by_a.items():
        lst.sort(key=lambda x: x["score"], reverse=True)
        gold_positions = [i for i, r in enumerate(lst) if r["label"] >= 0.5]
        if not gold_positions:
            continue
        top = max(gold_positions)
        for k in k_values:
            hr[k].append(1.0 if min(gold_positions) < k else 0.0)
        # NDCG@5（单个黄金）
        dcg = 1.0 / np.log2(top + 2)
        idcg = 1.0
        ndcg5.append(dcg / idcg)
    out = {}
    for k in k_values:
        out[f"HR@{k}"] = round(float(np.mean(hr[k])), 4) if hr[k] else None
    out["NDCG@5"] = round(float(np.mean(ndcg5)), 4) if ndcg5 else None
    return out


def check_holdout(model, holdout_path):
    """holdout 陷阱：每个断言 level_le 表示 focal 对 counterpart 的分应 ≤ max。
    由于我们的模型只输出一个分，用平均方向分近似 level；断言通过 = 平均分 ≤ max+0.05。
    """
    rows = []
    with open(holdout_path, encoding="utf-8") as f:
        items = [json.loads(line) for line in f]
    results = []
    for it in items:
        aid, bid = it["profiles"]["A"], it["profiles"]["B"]
        sc = score_pairs(model, [(aid, bid)])[0]
        ok = True
        detail = []
        for a in it.get("assertions", []):
            if a.get("type") == "level_le" and a.get("focal") == "A":
                maxv = a["max"]
                passed = sc <= maxv + 0.05
                ok = ok and passed
                detail.append(f"{a['id']}: avg={sc:.3f} max={maxv} {'PASS' if passed else 'FAIL'}")
        results.append({"id": it["id"], "title": it["title"], "score": round(sc, 3), "ok": ok, "detail": detail})
    n_ok = sum(1 for r in results if r["ok"])
    return results, n_ok, len(results)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--model", required=True)
    ap.add_argument("--data", required=True, help="prepare_data.py 的 --out-dir")
    ap.add_argument("--out", default=None, help="评测报告输出路径")
    args = ap.parse_args()

    data = Path(args.data)
    model = CrossEncoder(args.model, num_labels=1, max_length=512)

    # 1) test.jsonl 指标
    rows = []
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
            # AUC
            y = np.array([r["label"] for r in test_rows])
            s = np.array([r["score"] for r in test_rows])
            n_pos = (y == 1).sum()
            n_neg = (y == 0).sum()
            if n_pos > 0 and n_neg > 0:
                rng = np.random.default_rng(0)
                pos = s[y == 1]
                neg = s[y == 0]
                auc = (pos[:, None] > neg[None, :]).mean()
                metrics["AUC"] = round(float(auc), 4)
            print(f"[evaluate] test.jsonl: {len(test_rows)} 对")
            for k, v in metrics.items():
                print(f"  {k}: {v}")
        else:
            metrics = {}
            print("[evaluate] test.jsonl 为空，跳过")
    else:
        metrics = {}
        print("[evaluate] 无 test.jsonl")

    # 2) holdout 陷阱
    holdout_path = data / "holdout.jsonl"
    holdout_res = []
    n_ok = 0
    n_tot = 0
    if holdout_path.exists():
        holdout_res, n_ok, n_tot = check_holdout(model, holdout_path)
        print(f"[evaluate] holdout 陷阱: {n_ok}/{n_tot} 通过")

    report = {
        "model": args.model,
        "metrics": metrics,
        "holdout": {"passed": n_ok, "total": n_tot, "results": holdout_res},
    }
    if args.out:
        Path(args.out).write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
        print(f"[evaluate] 报告已写入 {args.out}")
    else:
        print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
