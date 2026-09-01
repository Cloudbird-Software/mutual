#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
mutual 训练数据准备器
====================
把仓库内的合成数据（data/bench + data/bench-extended + holdout）转成
训练 / 评测格式，供 train_reranker.py / evaluate_reranker.py 使用。

输出（--out-dir）：
  train.jsonl / val.jsonl / test.jsonl   P0 Score 打分器训练/验证/测试集
  holdout.jsonl                          12 陷阱评测集（只用于评测，永不入训练）
  baseline.json                          训练前 zero-shot 基线占位（由 evaluate 写）

每行样本格式：
  {"pair_id": "classic__m0__p0", "scenario": "classic",
   "a_id": "m0", "b_id": "p0", "label": 1.0,
   "a": "skills: ...\nvision: ...\n...", "b": "..."}
  label = 1.0 黄金对 / 0.0 非黄金对（score 训练用二分类或回归都可）
  可选：--synthesize 生成合成画像对（扩展规模）

用法：
  python prepare_data.py --out-dir ./output/data
  python prepare_data.py --out-dir ./output/data --synthesize --synthetic-pairs 5000
"""
import argparse
import json
import random
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
BENCH_DIR = REPO_ROOT / "data" / "bench"
EXT_DIR = REPO_ROOT / "data" / "bench-extended"
HOLDOUT_DIR = REPO_ROOT / "holdout" / "scenarios"

CANONICAL_SECTIONS = ["skills", "vision", "project", "needs"]


def load_json(p):
    with open(p, encoding="utf-8") as f:
        return json.load(f)


def format_sections(profile: dict) -> str:
    """把 profile 的 sections 序列化为 'key: value' 换行（与引擎 FormatSections 同构）。"""
    sections = profile if all(k in profile for k in CANONICAL_SECTIONS) else profile.get("sections", profile)
    lines = []
    for k in CANONICAL_SECTIONS:
        v = sections.get(k)
        if v is None:
            v = "Not specified"
        lines.append(f"{k}: {v}")
    return "\n".join(lines)


def scenario_pairs(scenario: dict, scenario_name: str):
    """从单个 bench 场景生成 (pair_id, a_id, b_id, label, a_text, b_text) 列表。
    正样本 = ground_truth 黄金对；负样本 = 同一场景内非黄金对（下采样控制规模）。
    """
    members = scenario["members"]
    pool = scenario["pool"]
    gt = scenario.get("ground_truth", {})
    pairs = []

    def _gt_ids(m_id):
        """ground_truth 值可能是单个 id 字符串或 id 列表，统一规范化为 list[str]。"""
        v = gt.get(m_id, [])
        if isinstance(v, str):
            return [v]
        if isinstance(v, list):
            return [x for x in v if isinstance(x, str)]
        return []

    # 黄金对
    for m_id in gt:
        for p_id in _gt_ids(m_id):
            pairs.append({
                "pair_id": f"{scenario_name}__{m_id}__{p_id}", "scenario": scenario_name,
                "a_id": m_id, "b_id": p_id, "label": 1.0,
                "a": format_sections(members[m_id]), "b": format_sections(pool[p_id]),
            })
    # 非黄金对（负样本）：每个 member 取有限个干扰 pool
    for m_id in members:
        gold_set = set(_gt_ids(m_id))
        neg_pool = [p for p in pool if p not in gold_set]
        random.shuffle(neg_pool)
        for p_id in neg_pool[:3]:  # 每个 member 最多 3 个干扰对，控制规模
            pairs.append({
                "pair_id": f"{scenario_name}__{m_id}__{p_id}", "scenario": scenario_name,
                "a_id": m_id, "b_id": p_id, "label": 0.0,
                "a": format_sections(members[m_id]), "b": format_sections(pool[p_id]),
            })
    return pairs


def load_all_bench_pairs():
    pairs = []
    for f in sorted(BENCH_DIR.glob("*.json")):
        pairs.extend(scenario_pairs(load_json(f), f.stem))
    for f in sorted(EXT_DIR.glob("*.json")):
        d = load_json(f)
        if "members" in d and "pool" in d:
            pairs.extend(scenario_pairs(d, f.stem))
    return pairs


def synthesize_pairs(n: int, seed: int = 42) -> list:
    """合成画像对：基于 30 基词 × 17 变体 aspect 槽（对齐实验文档 lab/gen 设计）。
    生成"互补"的黄金对（A.needs ↔ B.skills 命中同一 aspect），并保证词法多样性。
    """
    rng = random.Random(seed)
    aspects = []
    base_words = ["AI", "大数据", "供应链", "金融科技", "跨境电商", "新能源", "智能制造",
                  "营销", "合规", "物流", "医疗", "教育", "文旅", "建筑", "农业", "软件",
                  "硬件", "物联网", "云计算", "网络安全", "设计", "法务", "财税", "人力",
                  "渠道", "品牌", "外贸", "内销", "出海", "本地化"]
    variants = ["落地", "交付", "深耕", "转型", "扩张", "升级", "集成", "方案",
                "服务", "平台", "生态", "资源", "团队", "产能", "资质", "经验", "标准"]
    for w in base_words:
        for v in variants:
            aspects.append((w, v))
    pairs = []
    for i in range(n):
        w, v = rng.choice(aspects)
        m_id = f"syn_m{i}"
        p_id = f"syn_p{i}"
        pairs.append({
            "pair_id": f"synthetic__{m_id}__{p_id}", "scenario": "synthetic",
            "a_id": m_id, "b_id": p_id, "label": 1.0,
            "a": format_sections({
                "skills": f"{w}领域{v}能力，团队完备", "vision": f"{w}长期主义",
                "project": f"{w}项目第1期{v}", "needs": f"急需{w}{v}能力合作伙伴"}),
            "b": format_sections({
                "skills": f"资深{w}{v}专家，交付团队完备", "vision": f"{w}深耕",
                "project": f"{w}{v}落地经验", "needs": f"寻找{w}方向应用场景"}),
        })
        # 加一个干扰负样本
        w2 = rng.choice(base_words)
        while w2 == w:
            w2 = rng.choice(base_words)
        v2 = rng.choice(variants)
        pairs.append({
            "pair_id": f"synthetic__{m_id}__syn_d{i}", "scenario": "synthetic",
            "a_id": m_id, "b_id": f"syn_d{i}", "label": 0.0,
            "a": format_sections({
                "skills": f"{w}领域{v}能力", "vision": f"{w}长期主义",
                "project": f"{w}项目", "needs": f"急需{w}{v}合作伙伴"}),
            "b": format_sections({
                "skills": f"{w2}{v2}专家", "vision": f"{w2}方向",
                "project": f"{w2}项目", "needs": f"找{w2}场景"}),
        })
    return pairs


def load_holdout():
    """holdout 陷阱：只作为评测集导出（含断言，评测脚本消费）。"""
    items = []
    for f in sorted(HOLDOUT_DIR.glob("HT-*.json")):
        d = load_json(f)
        items.append(d)
    return items


def split(pairs, ratios=(0.8, 0.1, 0.1), seed=42):
    rng = random.Random(seed)
    # 按 scenario 分组保证隔离
    by_scn = {}
    for p in pairs:
        by_scn.setdefault(p["scenario"], []).append(p)
    train, val, test = [], [], []
    for scn, lst in by_scn.items():
        rng.shuffle(lst)
        n = len(lst)
        n_tr = int(n * ratios[0])
        n_va = int(n * ratios[1])
        train.extend(lst[:n_tr])
        val.extend(lst[n_tr:n_tr + n_va])
        test.extend(lst[n_tr + n_va:])
    return train, val, test


def write_jsonl(path, rows):
    with open(path, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--synthesize", action="store_true", help="额外生成合成画像对")
    ap.add_argument("--synthetic-pairs", type=int, default=5000)
    ap.add_argument("--seed", type=int, default=42)
    args = ap.parse_args()

    out = Path(args.out_dir)
    out.mkdir(parents=True, exist_ok=True)

    pairs = load_all_bench_pairs()
    print(f"[prepare_data] 从 bench/extended 加载 {len(pairs)} 对")

    if args.synthesize:
        synth = synthesize_pairs(args.synthetic_pairs, seed=args.seed)
        pairs.extend(synth)
        print(f"[prepare_data] 合成扩展 +{len(synth)} 对 → 共 {len(pairs)} 对")

    train, val, test = split(pairs, seed=args.seed)
    write_jsonl(out / "train.jsonl", train)
    write_jsonl(out / "val.jsonl", val)
    write_jsonl(out / "test.jsonl", test)
    print(f"[prepare_data] train={len(train)} val={len(val)} test={len(test)}")

    holdout = load_holdout()
    write_jsonl(out / "holdout.jsonl", holdout)
    print(f"[prepare_data] holdout 陷阱 = {len(holdout)} 个（仅评测）")

    # 基线占位
    if not (out / "baseline.json").exists():
        write_jsonl(out / "baseline.json", [])
    print(f"[prepare_data] 完成 → {out}")


if __name__ == "__main__":
    main()
