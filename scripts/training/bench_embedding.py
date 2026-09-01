#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
mutual P1 Embedding 替换实测脚本
==============================
对比不同 embedding 模型在 bench + zh_assoc 画像上的召回质量，
支撑"是否替换 voyage-3-lite"的决策（TRAINING_SPEC §2）。

用法：
  python bench_embedding.py --data ./output/data
  # 指定要测的模型：
  python bench_embedding.py --data ./output/data \
      --models BAAI/bge-m3 shibing624/text2vec-base-chinese
"""
import argparse
import json
from pathlib import Path

import numpy as np
from sentence_transformers import SentenceTransformer

CANONICAL_SECTIONS = ["skills", "vision", "project", "needs"]


def format_sections(profile) -> str:
    sections = profile if all(k in profile for k in CANONICAL_SECTIONS) else profile.get("sections", profile)
    return "\n".join(f"{k}: {sections.get(k, 'Not specified')}" for k in CANONICAL_SECTIONS)


def load_profiles(data_dir):
    """从 train/val/test 提取 (id, text) 与黄金对。"""
    profiles = {}
    gold_pairs = []
    for fname in ("train.jsonl", "val.jsonl", "test.jsonl"):
        p = Path(data_dir) / fname
        if not p.exists():
            continue
        with open(p, encoding="utf-8") as f:
            for line in f:
                r = json.loads(line)
                profiles[r["a_id"]] = r["a"]
                profiles[r["b_id"]] = r["b"]
                if r["label"] >= 0.5:
                    gold_pairs.append((r["a_id"], r["b_id"]))
    return profiles, gold_pairs


def recall_at_k(embeddings, ids, gold_pairs, k=20):
    """对每个 a 检索 top-k，golden b 是否在 top-k。"""
    em = np.array(embeddings)
    norm = em / (np.linalg.norm(em, axis=1, keepdims=True) + 1e-9)
    sim = norm @ norm.T
    id_to_idx = {i: j for j, i in enumerate(ids)}
    hits = []
    for a, b in gold_pairs:
        if a not in id_to_idx or b not in id_to_idx:
            continue
        ia, ib = id_to_idx[a], id_to_idx[b]
        order = np.argsort(-sim[ia])[:k]
        hits.append(1.0 if ib in order else 0.0)
    return float(np.mean(hits)) if hits else None


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--data", required=True)
    ap.add_argument("--models", nargs="+", default=["BAAI/bge-m3"])
    ap.add_argument("--k", type=int, default=20)
    args = ap.parse_args()

    profiles, gold_pairs = load_profiles(args.data)
    ids = list(profiles.keys())
    texts = [profiles[i] for i in ids]
    print(f"[bench_embedding] {len(ids)} 画像, {len(gold_pairs)} 黄金对")

    for m in args.models:
        try:
            model = SentenceTransformer(m)
            embs = model.encode(texts, batch_size=32, show_progress_bar=False, normalize_embeddings=True)
            rk = recall_at_k(embs, ids, gold_pairs, k=args.k)
            print(f"[bench_embedding] {m}: recall@{args.k} = {rk}")
        except Exception as e:
            print(f"[bench_embedding] {m}: 加载/推理失败 — {e}")


if __name__ == "__main__":
    main()
