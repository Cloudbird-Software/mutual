#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
mutual P0 Score 双向打分器训练脚本
=================================
基于 sentence-transformers CrossEncoder 微调，输出双向互补打分能力。

设计（对齐 docs/training/TRAINING_SPEC.md §1）：
  - 方向前缀方案（方案 A）：同一画像对生成两个方向样本——
    a_to_b: "A needs B provides: <A四节> [SEP] <B四节>" → label = a_to_b
    b_to_a: "A needs B provides: <B四节> [SEP] <A四节>" → label = b_to_a
    （方向语义 = 对方 skills 满足本方 needs 的互补匹配，非对称相似度）
  - 训练数据来自 prepare_data.py 输出（train.jsonl/val.jsonl）
  - 回归 MSE loss；基座默认 bge-reranker-v2-m3（可换 Qwen3-Reranker-0.6B）

用法：
  python train_reranker.py --base-model BAAI/bge-reranker-v2-m3 \
      --data ./output/data --out-dir ./output/model
"""
import argparse
import json
import os
import sys
from pathlib import Path

import torch
from torch.utils.data import DataLoader, Dataset
from sentence_transformers import CrossEncoder
from sentence_transformers.cross_encoder.losses import BinaryCrossEntropyLoss


class PairScoreDataset(Dataset):
    """从 train.jsonl/val.jsonl 构造双向方向样本。
    每对 (a,b) 产生两个方向样本：(a→b) 与 (b→a)，label 均为该对得分。
    双向打分 = 换向输入（a_to_b 用 (a,b)，b_to_a 用 (b,a)），
    等价于方向前缀方案（TRAINING_SPEC §1.3 方案 A）。
    """

    def __init__(self, jsonl_path):
        self.examples = []
        with open(jsonl_path, encoding="utf-8") as f:
            for line in f:
                row = json.loads(line)
                if row.get("label") is None:
                    continue
                a, b, lab = row["a"], row["b"], float(row["label"])
                # 两个方向样本（互补匹配非对称，换向输入即换向打分）
                self.examples.append((a, b, lab))
                self.examples.append((b, a, lab))

    def __len__(self):
        return len(self.examples)

    def __getitem__(self, i):
        a, b, lab = self.examples[i]
        return a, b, lab


class RerankerTrainer:
    def __init__(self, base_model):
        self.base_model = base_model
        self.device = "cuda" if torch.cuda.is_available() else "cpu"
        self.model = CrossEncoder(base_model, num_labels=1, max_length=512)
        print(f"[train_reranker] 基座 {base_model} 加载完成，device={self.device}")

    def train(self, train_path, val_path, out_dir, epochs=4, batch_size=16,
              lr=2e-5, warmup_ratio=0.1):
        train_ds = PairScoreDataset(train_path)
        val_ds = PairScoreDataset(val_path)

        print(f"[train_reranker] train={len(train_ds)} 条（双向样本）, val={len(val_ds)} 条")

        train_dl = DataLoader(train_ds, batch_size=batch_size, shuffle=True)
        val_dl = DataLoader(val_ds, batch_size=batch_size)

        # CrossEncoder.fit 的语义：label ∈ {0,1} 用 BinaryCrossEntropyLoss。
        # 注意：当前 label 来自黄金对(=1)/非黄金对(=0)，如需回归连续分，
        # 请改用 sentence_transformers.cross_encoder.losses 的回归 loss。
        self.model.fit(
            train_dataloader=train_dl,
            loss=BinaryCrossEntropyLoss(model=self.model),
            epochs=epochs,
            warmup_steps=int(len(train_dl) * epochs * warmup_ratio),
            optimizer_params={"lr": lr},
            output_path=str(out_dir),
        )
        print(f"[train_reranker] 训练完成 → {out_dir}")

    def save(self, out_dir):
        self.model.save_pretrained(str(out_dir))
        print(f"[train_reranker] 模型已保存 → {out_dir}")


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--base-model", default="BAAI/bge-reranker-v2-m3")
    ap.add_argument("--data", required=True, help="prepare_data.py 的 --out-dir")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--epochs", type=int, default=4)
    ap.add_argument("--batch-size", type=int, default=16)
    ap.add_argument("--lr", type=float, default=2e-5)
    args = ap.parse_args()

    data = Path(args.data)
    for name in ("train.jsonl", "val.jsonl"):
        if not (data / name).exists():
            print(f"[train_reranker] 缺少 {data/name}，先跑 prepare_data.py", file=sys.stderr)
            sys.exit(1)

    Path(args.out_dir).mkdir(parents=True, exist_ok=True)
    trainer = RerankerTrainer(args.base_model)
    trainer.train(
        train_path=data / "train.jsonl",
        val_path=data / "val.jsonl",
        out_dir=args.out_dir,
        epochs=args.epochs,
        batch_size=args.batch_size,
        lr=args.lr,
    )
    trainer.save(args.out_dir)
    print(f"[train_reranker] 完成。下一步：evaluate_reranker.py --model {args.out_dir}")


if __name__ == "__main__":
    main()
