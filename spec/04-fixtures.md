# 04-Fixtures：Golden 测试规范

> Fixture = 固定输入 → 固定输出的可执行 spec。
> 实现代码重写后，必须逐位通过 golden fixture，否则视为 spec 违约。

## 1. 目录结构

```
tests/golden/
├── test_basic/              # 基础互识场景（4 人 cohort）
│   ├── alice.json
│   ├── bob.json
│   ├── carol.json
│   ├── david.json
│   └── cohort.json         # 期望的全量匹配结果
├── test_reciprocal/         # 互惠匹配场景（合成市场）
│   └── market_30x20.json   # FairRec 合成市场 + 期望匹配
└── test_cold_start/         # 冷启动场景（新实体，规划项 Phase 2，当前未落盘）
    └── new_member.json
```

## 2. Fixture 文件格式

### 2.1 用户 Profile Fixture

```json
{
  "id": "alice",
  "sections": {
    "skills": "Visual arts specializing in...",
    "vision": "Passionate about leveraging art...",
    "project": "Current focus is on...",
    "needs": "Looking for technical collaborators..."
  },
  "last_updated_at": "2026-08-14T00:00:00Z"
}
```

### 2.2 Cohort 期望结果 Fixture

```json
{
  "overview": {
    "total_users": 4,
    "total_edges": 6,
    "average_degree": 3.0
  },
  "score_statistics": {
    "final_weights": { "min": 0.0, "max": 0.863, "avg": 0.581 }
  },
  "users": {
    "alice": {
      "degree": 3,
      "matches": [
        {
          "partner": "bob",
          "weight": 0.863,
          "directional_scores": {
            "alice_to_bob": 0.85,
            "bob_to_alice": 0.90
          }
        }
      ]
    }
  }
}
```

### 2.3 合成市场 Fixture

```json
{
  "market_config": { "num_left": 30, "num_right": 20, "pref_seed": 0 },
  "expected": {
    "total_matches": 20,
    "envy_count_left": 0,
    "envy_count_right": 0
  }
}
```

## 3. 添加新 Fixture 的规则

1. **场景定义**：在 `spec/04-fixtures.md` 描述场景目的。
2. **输入固定**：profile/market 数据写入 `tests/golden/<scenario>/`。
3. **期望输出**：用当前实现生成输出，人工审核后固化为期望值。
4. **回归测试**：在 `tests/test_golden.py` 添加测试函数。
5. **CI 强制**：golden test 失败 = CI 阻断。

## 4. 测试执行规则

- **离线优先**：golden test 不调用真实 LLM，用 fake LLM + fake embedder。
- **`RUN_LLM_TESTS=1`**：环境变量门控，仅手动触发时跑真实 LLM golden e2e。
- **确定性**：所有随机操作必须用固定 seed。

## 5. Fixture 与 Spec 的关系

- Fixture 是 **可执行的 spec**：它把"期望行为"从文字描述变成机器可验证的断言。
- 实现重写后 golden test 失败 → 说明实现偏离了 spec → 要么修实现，要么修 spec（如果 spec 本身有误）。
- **不允许**：为了让 golden test 通过而修改 fixture 期望值（除非 spec 变更并经过审核）。

## 6. 期望值的生效分期

> 依据 spec/05-boundaries.md §11（golden 断言分层）。

| Fixture 字段 | 生效 | 说明 |
|---|---|---|
| `cohort.json` overview（total_edges=6 等）/ degree_distribution | Phase 1 | 算法无关，由度约束推得 |
| `cohort.json` directional_score_check | Phase 1 | 由 §7 fake 分数表驱动 |
| `cohort.json` score_statistics.llm_scores | Phase 1 | 与 §7 分数表自洽（min 0.35 / max 0.9 / avg 0.683） |
| `cohort.json` score_statistics.final_weights / embedding_scores | 暂缓 | 参考实现移植值；Phase 1 合入后按 §3.3 重新固化（走 spec 变更） |
| `cohort.json` envy_report | Phase 2 | 依赖 NSW 求解器 |
| `market_30x20.json` expected（total_matches=20 / envy=0） | Phase 2 | fixture note 明确锚定 `nsw_maximize` |

## 7. fake_llm / fake_embedder 确定性契约

> Golden 的期望值依赖 fake 的行为，因此 fake 的行为本身是 spec 的一部分
> （spec/05-boundaries.md §12）。实现位置：`tests/conftest.py`；守护测试：`tests/test_fakes.py`。
> 变更本契约 = spec 变更。

### 7.1 fake_llm

- `__call__(messages, **kwargs)` 是同步纯查表，**路由规则**由 prompt 文本决定：
  - prompt 含输出格式标记 `"a_to_b"`（打分类 prompt 必含，score 实现的 JSON 解析依赖它）
    → 打分类路径：识别 prompt 中出现的已知 cohort user_id
    （`alice`/`bob`/`carol`/`david`），取字典序最小两个组成 pair，按表返回。
  - 否则 → 非打分类路径（introduce 等）：返回固定模板
    `{"intro": "Fake intro.", "starter_topics": "Fake topic."}`。
- **打分分数表**（返回体 `{"a_to_b": <x>, "b_to_a": <y>, "reasoning": "fake"}`）：

| pair_id | a_to_b | b_to_a |
|---|---|---|
| alice__bob | 0.85 | 0.90 |
| alice__carol | 0.80 | 0.82 |
| bob__carol | 0.83 | 0.82 |
| alice__david | 0.52 | 0.63 |
| bob__david | 0.45 | 0.58 |
| carol__david | 0.35 | 0.65 |

  该表与 `cohort.json` 的 `directional_score_check` 及 `llm_scores` 统计自洽。
- **默认兜底**：打分类 prompt 未命中表（非 cohort 用户，如其他测试数据）返回
  `{"a_to_b": 0.5, "b_to_a": 0.5, "reasoning": "fake"}`（对称默认，不制造方向性假象）。
- **计数**：`call_count` 每次调用 +1；`cache_writes` 恒 0（fake 不写缓存）。

### 7.2 fake_embedder

- `get_embedding_model()` 返回的 embedder 满足 `embed(texts) -> [N, 128]` ndarray。
- 第 i 行向量 = `np.random.RandomState(int(hash_text(texts[i]), 16) % 2**32).randn(128)`，
  其中 `hash_text` = `mutual.schemas.hash_text`（sha256 前 16 hex，跨进程稳定）。
- **禁止**内置 `hash()` 做种子（进程间 salt 不同，embedding 不可复现；与 05-boundaries §5 同源原则）。
- 维度恒 128，与 `models.embedding_dimensions` 无关（fake 不模拟真实模型）。

### 7.3 确定性要求

- 同一 prompt / 同一 text，任意次调用、任意进程返回逐位一致。
- `tests/test_fakes.py` 守护：查表正确性、方向不对称、与 cohort.json 统计自洽、
  默认兜底、embedder 逐位确定性。
