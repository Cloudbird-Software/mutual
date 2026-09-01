#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
mutual P2 Extract 四节结构化器训练脚本（可选，LoRA 微调 Qwen2.5）
============================================================
把"自由文本画像 → 四节（skills/vision/project/needs）"训练成小模型，
消除 extract 提示词注入面（#52/#46），并降低线性成本。

数据：用 bench 四节画像拼回"自由文本画像"作输入，四节作监督标签。
生成：本脚本内置 make_free_text 自由文本拼装函数，直接导出 LLaMA-Factory
     数据集（extract_data.jsonl），无需依赖 prepare_data.py 的额外参数。

用法：
  python train_extract.py --data ./output/data --out-dir ./output/extract-model \
      --base-model Qwen/Qwen2.5-0.5B-Instruct
"""
import argparse
import json
from pathlib import Path

CANONICAL_SECTIONS = ["skills", "vision", "project", "needs"]


def make_free_text(sections) -> str:
    """把结构化四节拼回一段自由文本画像（模拟真实输入）。"""
    s = sections
    parts = [
        f"我的核心能力包括：{s.get('skills', '')}。",
        f"目前在做：{s.get('project', '')}。",
        f"长期愿景是：{s.get('vision', '')}。",
        f"我现在最需要的是：{s.get('needs', '')}。",
    ]
    return " ".join(parts)


def export_llamafactory_data(data_dir, out_dir):
    """导出 LLaMA-Factory 数据集：extract_data.jsonl + dataset_info.json。
    LLaMA-Factory 的 --dataset_dir 目录内必须有 dataset_info.json 注册数据集，
    否则 --dataset extract_data 会报"未注册数据集"错误。
    instruction/input/output 三段式；output 为四节 JSON 字符串。
    """
    rows = []
    for fname in ("train.jsonl", "val.jsonl"):
        p = Path(data_dir) / fname
        if not p.exists():
            continue
        with open(p, encoding="utf-8") as f:
            for line in f:
                r = json.loads(line)
                # 用 r["a"] 的 sections 反推自由文本
                # r["a"] 是 'key: value' 多行，需解析回 sections
                sections = parse_key_value(r["a"])
                free_text = make_free_text(sections)
                output = json.dumps(sections, ensure_ascii=False)
                rows.append({
                    "instruction": "从以下中文自由文本画像中提取四个分节：skills（技能）、vision（愿景）、project（项目）、needs（需求）。只输出 JSON。",
                    "input": free_text,
                    "output": output,
                })
    out_path = Path(out_dir) / "extract_data.jsonl"
    with open(out_path, "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")
    # dataset_info.json：注册 extract_data，LLaMA-Factory 必需
    info_path = Path(out_dir) / "dataset_info.json"
    info = {
        "extract_data": {
            "file_name": "extract_data.jsonl",
            "columns": {"prompt": "instruction", "query": "input", "response": "output"},
        }
    }
    with open(info_path, "w", encoding="utf-8") as f:
        json.dump(info, f, ensure_ascii=False, indent=2)
    print(f"[train_extract] 已导出 {len(rows)} 条到 {out_path}")
    print(f"[train_extract] dataset_info.json 已生成（注册 extract_data 数据集）")


def parse_key_value(text):
    """把 'key: value' 多行解析回 dict。"""
    out = {}
    for line in text.splitlines():
        if ": " in line:
            k, v = line.split(": ", 1)
            out[k.strip()] = v.strip()
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--data", required=True, help="prepare_data.py 的 --out-dir")
    ap.add_argument("--out-dir", required=True)
    ap.add_argument("--base-model", default="Qwen/Qwen2.5-0.5B-Instruct")
    args = ap.parse_args()

    Path(args.out_dir).mkdir(parents=True, exist_ok=True)
    data_path = Path(args.data)

    # 1) 导出 LLaMA-Factory 数据集（extract_data.jsonl + dataset_info.json）
    export_llamafactory_data(data_path, Path(args.out_dir))

    # 2) 打印训练命令（LLaMA-Factory CLI 由 PM 在显卡环境执行）
    print()
    print("[train_extract] 请在显卡环境用 LLaMA-Factory 执行：")
    print(f"""
pip install llamafactory
llamafactory-cli train \\
  --model_name_or_path {args.base_model} \\
  --dataset_dir {args.out_dir} \\
  --dataset extract_data \\
  --template qwen \\
  --finetuning_type lora \\
  --output_dir {args.out_dir}/lora \\
  --num_train_epochs 5.0 \\
  --learning_rate 1e-4 \\
  --per_device_train_batch_size 8 \\
  --gradient_accumulation_steps 2 \\
  --lora_rank 16 \\
  --logging_steps 10 --save_steps 500
""")
    print("[train_extract] 合并 LoRA 后即可接入引擎 CompleteExtract（见 MODEL_SERVING.md §3）")


if __name__ == "__main__":
    main()
