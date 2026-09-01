#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
mutual Score 打分器本地推理服务
==============================
把训练好的 cross-encoder 暴露为 HTTP 服务，供 Go 引擎的 RerankerScoreClient 调用
（见 docs/training/MODEL_SERVING.md）。

接口：
  POST /predict         单对打分 → {"a_to_b": .., "b_to_a": ..}
  POST /predict_batch   批量对打分 → [{"a_to_b": .., "b_to_a": ..}, ...]
  GET  /health          健康检查

启动：
  python serve_reranker.py --model ./output/model --port 8000
"""
import argparse
import json
from pathlib import Path

from flask import Flask, request, jsonify
from sentence_transformers import CrossEncoder

app = Flask(__name__)
MODEL = None

CANONICAL_SECTIONS = ["skills", "vision", "project", "needs"]


def format_sections(profile) -> str:
    sections = profile if all(k in profile for k in CANONICAL_SECTIONS) else profile.get("sections", profile)
    lines = []
    for k in CANONICAL_SECTIONS:
        v = sections.get(k)
        lines.append(f"{k}: {v if v is not None else 'Not specified'}")
    return "\n".join(lines)


def score_one(a_text, b_text):
    """a_to_b：A 需要 × B 提供 → (a, b)；b_to_a：(b, a) 换向打分。"""
    s_ab = MODEL.predict([(a_text, b_text)], show_progress_bar=False)[0]
    s_ba = MODEL.predict([(b_text, a_text)], show_progress_bar=False)[0]
    # clamp 到 [0,1]（cross-encoder logit 可能越界）
    return max(0.0, min(1.0, float(s_ab))), max(0.0, min(1.0, float(s_ba)))


@app.route("/health", methods=["GET"])
def health():
    return jsonify({"status": "ok", "model": MODEL_NAME})


@app.route("/predict", methods=["POST"])
def predict():
    body = request.get_json(force=True)
    a = format_sections(body.get("a_sections", body.get("a", {})))
    b = format_sections(body.get("b_sections", body.get("b", {})))
    ab, ba = score_one(a, b)
    return jsonify({"a_to_b": ab, "b_to_a": ba})


@app.route("/predict_batch", methods=["POST"])
def predict_batch():
    body = request.get_json(force=True)
    pairs = body.get("pairs", [])
    out = []
    for p in pairs:
        a = format_sections(p.get("a_sections", p.get("a", {})))
        b = format_sections(p.get("b_sections", p.get("b", {})))
        ab, ba = score_one(a, b)
        out.append({"a_to_b": ab, "b_to_a": ba})
    return jsonify({"scores": out})


def main():
    global MODEL, MODEL_NAME
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--model", required=True)
    ap.add_argument("--port", type=int, default=8000)
    ap.add_argument("--host", default="127.0.0.1")
    args = ap.parse_args()

    MODEL_NAME = args.model
    MODEL = CrossEncoder(args.model, num_labels=1, max_length=512)
    print(f"[serve] 模型 {args.model} 已加载，监听 {args.host}:{args.port}")
    app.run(host=args.host, port=args.port)


if __name__ == "__main__":
    main()
