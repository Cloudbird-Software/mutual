"""Mutual — LLM 自我改进反馈注入（Phase 3，docs/engineering-plan.md §5.2）。

三种反馈层级（spec/03-oracles.md §4）：

1. **Prompt 校准**（:func:`calibrate_prompt`）：评测报告的 HR/NDCG 历史写回
   打分 prompt 头部（few-shot 式信号），LLM 在下一轮打分时"知道"上一轮
   的推荐质量偏差方向。
2. **权重校准**（:func:`calibrate_weights`）：HR 下降时调整
   ``blending.embed_weight / llm_weight``（有界步进，禁止越界翻转）。
3. **Agent 记忆**（:class:`MatchMemory`）：记录接受/拒绝的匹配对，
   供后续轮次注入 prompt 或作 novelty 排除。

全部纯函数 / 内存对象，无 IO；持久化由调用方（runners/CLI）决定。
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Optional

from .schemas import EvaluationReport

# 权重校准边界与步长（有界，防止校准发散）
_W_MIN, _W_MAX = 0.1, 0.9
_W_STEP = 0.05


# ---------------------------------------------------------------------------
# 1. Prompt 校准
# ---------------------------------------------------------------------------


def calibrate_prompt(
    template: str,
    history: List[EvaluationReport],
    max_entries: int = 3,
) -> str:
    """把近期评测结果作为校准信号写入 prompt 头部。

    Args:
        template: 原始打分 prompt 模板。
        history: 按时间升序的评测报告（取末尾 ``max_entries`` 条）。
        max_entries: 注入的最大条数。

    Returns:
        头部带校准块的 prompt（history 为空时原样返回）。
    """
    if not history:
        return template
    lines = ["[Calibration] Recent evaluation feedback:"]
    for r in history[-max_entries:]:
        trend = _trend(r)
        lines.append(
            f"- HR@3={r.hr_at_3:.2f} NDCG@5={r.ndcg_at_5:.2f} envy={r.total_envy} "
            f"quality={trend}; "
            "reward reciprocal pairs where BOTH directions are strong; "
            "penalize one-directional attraction."
        )
    return "\n".join(lines) + "\n\n" + template


def _trend(report: EvaluationReport) -> str:
    """粗粒度质量分档（供 prompt 校准措辞）。"""
    if report.hr_at_3 >= 0.8:
        return "high"
    if report.hr_at_3 >= 0.5:
        return "medium"
    return "low"


# ---------------------------------------------------------------------------
# 2. 权重校准
# ---------------------------------------------------------------------------


def calibrate_weights(
    blending: Dict[str, float],
    current: EvaluationReport,
    previous: Optional[EvaluationReport] = None,
    step: float = _W_STEP,
) -> Dict[str, float]:
    """按 HR 变化调整 embed/llm 混合权重（spec §4：HR 下降时触发）。

    规则（保守有界）：
    - 无历史或 HR 持平/上升 → 不动；
    - HR 下降 → ``llm_weight += step``、``embed_weight -= step``
      （打分信号不可信时加重语义/LLM 侧），并整体重归一化、截断到边界。

    Args:
        blending: ``{"embed_weight": w, "llm_weight": w}``。
        current: 本轮评测报告。
        previous: 上一轮评测报告（None = 首轮，不调整）。
        step: 单次调整步长。

    Returns:
        新的 blending dict（不修改入参）。
    """
    out = dict(blending)
    if previous is None:
        return out
    if current.hr_at_3 >= previous.hr_at_3:
        return out  # 未下降：不触发

    ew = float(out.get("embed_weight", 0.5)) - step
    lw = float(out.get("llm_weight", 0.5)) + step
    ew = min(max(ew, _W_MIN), _W_MAX)
    lw = min(max(lw, _W_MIN), _W_MAX)
    # 重归一化（和为 1；若越界截断导致和≠1，按比例缩放）
    total = ew + lw
    if total > 0:
        ew, lw = ew / total, lw / total
    out["embed_weight"], out["llm_weight"] = ew, lw
    return out


# ---------------------------------------------------------------------------
# 3. Agent 记忆（接受/拒绝的匹配）
# ---------------------------------------------------------------------------


@dataclass
class MatchMemory:
    """跨轮匹配反馈记忆（接受 / 拒绝），供 prompt 注入或 novelty 排除。"""

    entries: List[Dict[str, Any]] = field(default_factory=list)

    def record(self, pair_id: str, accepted: bool, reason: str = "") -> None:
        """记录一次匹配反馈。"""
        self.entries.append({"pair_id": pair_id, "accepted": accepted, "reason": reason})

    @property
    def rejected_pair_ids(self) -> List[str]:
        """被拒绝的 pair_id（可并入 excluded_pairs 做 novelty 排除）。"""
        return [e["pair_id"] for e in self.entries if not e["accepted"]]

    def prompt_block(self, max_entries: int = 5) -> str:
        """近期反馈的 prompt 注入块（空记忆返回空串）。"""
        if not self.entries:
            return ""
        lines = ["[Memory] Recent match feedback:"]
        for e in self.entries[-max_entries:]:
            verdict = "ACCEPTED" if e["accepted"] else "REJECTED"
            reason = f" — {e['reason']}" if e.get("reason") else ""
            lines.append(f"- {e['pair_id']}: {verdict}{reason}")
        return "\n".join(lines)

    def save(self, path: Path) -> None:
        """持久化到 JSONL（append 语义由调用方控制，此处整写）。"""
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(
            "\n".join(json.dumps(e, ensure_ascii=False) for e in self.entries)
            + ("\n" if self.entries else ""),
            encoding="utf-8",
        )

    @classmethod
    def load(cls, path: Path) -> "MatchMemory":
        """从 JSONL 恢复。"""
        mem = cls()
        if not path.exists():
            return mem
        for line in path.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if line:
                mem.entries.append(json.loads(line))
        return mem
