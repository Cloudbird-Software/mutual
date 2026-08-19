"""捕获 engine 阶段的 golden 参考值（Go 重写差分测试的对拍基准）。

一次性工具：输出 golden/engine/full_flow.json，覆盖
similarity → select → score → pre_matrix → match → report 全链路。

确定性来源：
- golden embed bundle：tests/test_golden.py 的 _golden_embed 契约
  （RandomState(12345)，正相关向量）；
- fake_llm：tests/conftest.py 的打分表（§7.1）。

用法：.venv/bin/python scripts/capture_golden_engine.py
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

import numpy as np

sys.path.insert(0, "src")

from mutual.config import load_config, resolve_prompt_templates  # noqa: E402
from mutual.match import solve_match  # noqa: E402
from mutual.report import create_report  # noqa: E402
from mutual.schemas import EmbeddingsBundle  # noqa: E402
from mutual.score import (  # noqa: E402
    build_pref_matrix,
    create_sections_dict,
    prepare_normalized_scores,
    score_pairs_with_llm,
)
from mutual.select import select_pairs  # noqa: E402
from mutual.similarity import compute_similarity  # noqa: E402

_DIM = 8
_SEED = 12345


class FakeLLM:
    """与 tests/conftest.py fake_llm 相同契约的打分器。"""

    TABLE = {
        "alice__bob": {"a_to_b": 0.85, "b_to_a": 0.90},
        "alice__carol": {"a_to_b": 0.80, "b_to_a": 0.82},
        "bob__carol": {"a_to_b": 0.83, "b_to_a": 0.82},
        "alice__david": {"a_to_b": 0.52, "b_to_a": 0.63},
        "bob__david": {"a_to_b": 0.45, "b_to_a": 0.58},
        "carol__david": {"a_to_b": 0.35, "b_to_a": 0.65},
    }
    IDS = ("alice", "bob", "carol", "david")

    def __call__(self, messages, **kwargs):
        prompt_text = " ".join(str(m.get("content", "")) for m in messages)
        found = sorted(uid for uid in self.IDS if uid in prompt_text)
        if "a_to_b" in prompt_text and len(found) >= 2:
            entry = self.TABLE.get(f"{found[0]}__{found[1]}")
            if entry is not None:
                return json.dumps(
                    {"a_to_b": entry["a_to_b"], "b_to_a": entry["b_to_a"], "reasoning": "fake"}
                )
        if "a_to_b" in prompt_text:
            return '{"a_to_b": 0.5, "b_to_a": 0.5, "reasoning": "fake"}'
        return '{"intro": "Fake intro.", "starter_topics": "Fake topic."}'


def golden_embed(sections_list):
    """tests/test_golden.py 的 _golden_embed 契约（正相关向量）。"""
    names = sorted({n for es in sections_list for n in es.sections})
    n = len(sections_list)
    rng = np.random.RandomState(_SEED)
    base = rng.randn(n, len(names), _DIM)
    base[..., 0] += 5.0
    base /= np.linalg.norm(base, axis=-1, keepdims=True)
    return EmbeddingsBundle(
        user_ids=[es.id for es in sections_list],
        section_names=names,
        embeddings=base,
        hyde={name: np.zeros((n, 0, _DIM)) for name in names},
        embedding_model="golden-embedder",
        dim=_DIM,
    )


def main():
    golden_dir = Path("tests/golden/test_basic")
    profiles = []
    for f in sorted(golden_dir.glob("*.json")):
        if f.name == "cohort.json":
            continue
        profiles.append(json.loads(f.read_text(encoding="utf-8")))

    # extract 用画像自带 sections（golden 约定：不依赖 LLM）。
    from mutual.schemas import ExtractedSections

    extracted = [ExtractedSections(id=p["id"], sections=dict(p["sections"])) for p in profiles]
    bundle = golden_embed(extracted)

    config = load_config()
    config["budgets"]["n_profiles_to_score_together"] = 1
    recipe = config.get("recipe") or {}
    budgets = config.get("budgets") or {}

    # ---- similarity ----
    sim = compute_similarity(bundle, None, recipe)

    # ---- select ----
    selected = select_pairs(sim, budgets, excluded_pairs=None)

    # ---- score ----
    sections_dict = create_sections_dict(extracted)
    templates = resolve_prompt_templates(config)
    fake = FakeLLM()
    unscored = []
    scores = score_pairs_with_llm(
        selected,
        sections_dict,
        instruction=str(recipe.get("instruction", "")),
        prompt_template=templates["scoring"],
        llm_wrapper=fake,
        config=config,
        unscored_out=unscored,
    )
    scores = prepare_normalized_scores(scores, reference=None)

    # ---- pre_matrix + match ----
    pref = build_pref_matrix(scores, list(bundle.user_ids))
    edges, match_prob, envy = solve_match(
        pref,
        dict(config.get("matching") or {}),
        dict(config.get("blending") or {}),
        reference_scores=None,
    )

    # ---- report ----
    report = create_report(edges, extracted, 0, scope_user_ids=None)

    def mat(m):
        return [[float(v) for v in row] for row in np.asarray(m)]

    out = {
        "bundle": {
            "user_ids": list(bundle.user_ids),
            "section_names": list(bundle.section_names),
            "dim": bundle.dim,
            "embeddings": [[[float(v) for v in vec] for vec in user] for user in bundle.embeddings],
            "hyde_shapes": {k: list(v.shape) for k, v in bundle.hyde.items()},
        },
        "similarity": {
            "dir_matrix": mat(sim.dir_matrix),
            "fused_matrix": mat(sim.fused_matrix),
        },
        "selected_pairs": [
            {
                "user1": p.user1,
                "user2": p.user2,
                "pair_id": p.pair_id,
                "similarity_score": float(p.similarity_score),
            }
            for p in selected
        ],
        "pair_scores": {pid: ps.to_dict() for pid, ps in scores.items()},
        "pair_score_order": list(scores.keys()),
        "unscored_pair_ids": [p.pair_id for p in unscored],
        "pref_matrix": pref.to_dict(),
        "match": {
            "edges": [e.to_dict() for e in edges],
            "match_prob": mat(match_prob),
            "envy_report": envy,
        },
        "report": report,
    }

    out_path = Path("golden/engine/full_flow.json")
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(out, ensure_ascii=False, indent=1), encoding="utf-8")
    print(f"written {out_path} ({out_path.stat().st_size} bytes)")
    print(f"edges={len(edges)} selected={len(selected)} envy={envy['total_envy']}")


if __name__ == "__main__":
    main()
