# 工程方案：Mutual 双向互惠推荐引擎

> 本文档是 agent swarm 的施工蓝图。所有规划决策已在此固化，执行 agent 只需按步骤实现。

## 1. 总体策略

```
Phase 0 (已完成)     Phase 1 (2-3 周)        Phase 2 (2 周)        Phase 3 (2-3 周)
Spec 打底    →    可丢弃骨架跑通     →    互惠求解接入    →    评测闭环 + LLM 自改进
四件套 + CI       9 个 stage 实现        NSW + envy            Bench + 反馈注入
```

**原则**：每个 stage 一个 PR，CI 门禁强制，golden test 逐位回归。

## 2. Phase 0 — Spec 打底（✅ 已完成，剩余收尾）

### 已完成
- `spec/` 6 个文档（overview/schemas/stages/oracles/fixtures/boundaries）
- `src/mutual/schemas.py` — 12 个 dataclass
- `src/mutual/stages.py` — 11 个 StageSpec（stub）
- `src/mutual/config.py` — 配置加载器
- `config/default.yaml` — 默认配置
- `tests/golden/test_basic/` — 4 人 cohort fixture
- `tests/golden/test_reciprocal/` — 合成市场 fixture
- `tests/conftest.py` — 公共 fixture
- `CLAUDE.md` — agent 指令

### 剩余收尾（本批次完成）
| 任务 | 文件 | 说明 |
|---|---|---|
| 契约测试 | `tests/test_schemas.py` | 验证 dataclass to_dict/from_dict 往返 |
| 阶段注册测试 | `tests/test_stages.py` | 验证 11 个 stage 全部注册、describe_stage 可读 |
| 项目配置 | `pyproject.toml` | 依赖、ruff、mypy、pytest 配置 |
| CI pipeline | `.github/workflows/ci.yml` | lint + type check + golden test + 评测门禁 |
| CI 门禁文档 | `docs/ci-gates.md` | 门禁规则与 fail 行为 |
| PR 规则 | `CONTRIBUTING.md` | 开发流程、PR 模板、review checklist |
| 项目 README | `README.md` | 快速开始 |

## 3. Phase 1 — 可丢弃骨架跑通（2-3 周）

### 目标
以 Choreo 为参考，实现 9 个 stage 的 run/load/dump，用 golden fixture 做回归。此阶段代码可任意重写，契约不变。

### 实现顺序（严格按此）

> 模块归属总表：11 个注册 stage 与模块一一对应——
> extract/hyde/embed/similarity/select/score(含 pre_matrix)/introduce/report 是 Phase 1 模块；
> match/evaluate 是 Phase 2 模块；llm/store/runners 是 adapter 层模块（不在 StageSpec 注册表内）。

#### 3.1 `llm.py` — LLM Wrapper
```
输入: messages, model, temperature, max_tokens
输出: response_text
功能:
  - __call__: 同步调用 LLM API
  - run_coro_blocking: 从同步代码进入 asyncio（兼容宿主事件循环）
  - cache: 按完整 prompt hash (hash_text)，cache_dir=None 时禁用
  - cache_writes: 计数本次新缓存文件数
  - get_embedding_model: 返回 embedder
依赖: 无外部新依赖（用 openai SDK，已在 pyproject.toml）
参考: Choreo choreo/llm.py
```

#### 3.2 `store.py` — Store Protocol + FileStore
```
Store protocol:
  - get_sections(user_ids) → dict
  - put_sections(extracted)
  - get_embeddings() → EmbeddingsBundle | None
  - put_embeddings(bundle)
  - get_match_history() → list[dict]
  - put_matches(edges)

FileStore 实现:
  - 目录结构: {raw, processed, embeds, outputs, cache}
  - match_history.jsonl: append-only {pair_id, user1, user2, matched_at}
  - novelty_window_months 过滤

核心约定: core 阶段不调 Store，只有 runners 调。
参考: Choreo choreo/store.py
```

#### 3.3 `extract.py` — Profile 提取
```
输入: list[Profile], config
输出: list[ExtractedSections]
功能:
  - LLM 从自由文本提取 skills/vision/project/needs
  - 失败填 "Not specified"，failed_out 报告
  - hash 用 hash_text(json.dumps(sections, sort_keys=True))
边界: 失败结果不持久化（§4）
参考: Choreo choreo/extract.py
```

#### 3.4 `hyde.py` — HyDE 生成
```
输入: list[ExtractedSections], config (hyde.n_descriptors)
输出: dict[user_id → HydeDescriptors]
功能:
  - 为每个 section 生成 n_descriptors 个假设性描述
  - 增强语义匹配（如 "需要技术合作" → "寻找会 Python 的开发者"）
参考: Choreo choreo/hyde.py
```

#### 3.5 `embed.py` — Embedding 生成
```
输入: sections, hyde, config, existing_bundle
输出: EmbeddingsBundle
功能:
  - content-hash 驱动增量复用（只重嵌变化的 cell）
  - 不同 model 的 bundle 整体忽略
  - 全尺寸存储；MRL 截断在工作副本 (truncate_embeddings)
  - subset(ids) 原语
  - supports_mrl(model) 检查
边界: 复用是 content-addressed 不是 roster-addressed（§6）
参考: Choreo choreo/embed.py
```

#### 3.6 `similarity.py` — 方向性相似度
```
输入: source: EmbeddingsBundle, target: EmbeddingsBundle | None, recipe_config: dict
输出: SimilarityResult
功能:
  - 方向性 section 融合（section_weights + cross_section_weights）
  - target=None 时 N×N 方阵（legacy 路径 (dir+dir.T)/2 对称化），否则 M×N 矩形
  - 缺失 section = 中性（mask + 分母修正），不是零
公开签名: compute_similarity(source, target, recipe_config) -> SimilarityResult
参考: Choreo choreo/similarity.py
```

#### 3.7 `select.py` — 候选对选择
```
输入: similarity: SimilarityResult, budgets: dict, excluded_pairs: set[str] | None
输出: list[CandidatePair]
功能:
  - 贪心轮转选择，per-profile cap（max_n_llm_evaluations_per_profile）
  - 全局 cap（max_pair_llm_calls）
  - novelty 排除（excluded_pairs）+ 只保留正相似度对
公开签名: select_pairs(similarity, budgets, excluded_pairs) -> list[CandidatePair]
参考: Choreo choreo/select.py
```

#### 3.8 `score.py` — LLM 双向打分（含 pre_matrix）
```
输入: selected_pairs, sections_dict, instruction, prompt_template, llm_wrapper, config
输出: dict[pair_id → PairScore] + unscored_pairs (out-param)
功能:
  - LLM 对每对做 A→B 和 B→A 双向打分
  - 批量打分: n_profiles_to_score_together 控制每 prompt 对数
  - 预算控制: max_n_llm_evaluations_per_profile + max_pair_llm_calls
  - 缓存: 按完整 prompt hash (hash_text)
  - 归一化: reference_scores 驱动跨批次稳定归一化
边界:
  - 未打分候选保留 embedding 权重不丢弃（§3）
  - 缓存禁止用内置 hash()（§5）
pre_matrix（本模块内实现，见 §4.2）:
  公开签名: build_pref_matrix(pair_scores, all_user_ids) -> PrefMatrix
参考: Choreo choreo/score.py
```

#### 3.9 `introduce.py` — 对接话术
```
输入: edges, sections_dict, instruction, prompt_template, llm_wrapper
输出: dict[pair_id → Introduction]
功能:
  - 为每对匹配生成双向话术（For A: ... / For B: ...）
  - 生成破冰话题 (starter_topics)
  - LLM 失败时 attach_fallback_intro 生成模板话术
参考: Choreo choreo/introduction.py
```

#### 3.10 `report.py` — 匹配报告
```
输入: edges: list[Edge], extracted: list[ExtractedSections], top_matches_per_user: int, scope_user_ids: list[str] | None
输出: dict（用户报告 + 群组摘要）
功能:
  - 每用户 top-N 匹配列表（按 final_weight 排序）
  - 群组摘要（总边数、平均度、分数统计）
  - scope_user_ids 限定报告范围（batch 模式只报 member 侧）
公开签名: create_report(edges, extracted, top_matches_per_user, scope_user_ids) -> dict
```

#### 3.11 `runners.py` — 模式运行器
```
三种模式:
  - run_full_match(profiles/bundle, config, store=None) → MatchResult
    N×N 全量匹配
  - run_query_match(query_text, pool_bundle, config) → MatchResult
    1×M 查询匹配
  - run_batch_match(member_ids, pool_bundle, config, excluded_pairs) → BatchMatchResult
    M×N 子集批量匹配（互惠推荐的主模式）

约定:
  - schema in, schema out
  - store=None 时全内存运行
  - 串联: extract → hyde → embed → similarity → select → score → pre_matrix → match → introduce → report
参考: Choreo choreo/runners.py + choreo/batch_match.py
```

#### 3.12 `tests/test_golden.py` — Golden 回归测试（断言分层）
```
Phase 1 断言（算法无关不变量，离线 fake_llm + fake_embedder）:
  - test_basic_cohort: 4 人 cohort → 期望 6 条 Edge、度分布 {"3": 4}
  - test_directional_scores: A→B ≠ B→A（分数来自 spec/04-fixtures.md §7 fake 分数表）
  - test_determinism: 同输入两次运行输出逐位一致
  - test_intro_fallback: LLM 失败时模板话术兜底
Phase 2 激活断言（NSW 求解器相关）:
  - test_no_envy: envy_count = 0
  - test_reciprocal_market: 合成市场 30×20 → total_matches=20, envy=0
暂缓断言（不作为 Phase 1 门禁）:
  - cohort.json 的 score_statistics.final_weights / embedding_scores
    （参考实现移植值，Phase 1 合入后按 spec/04-fixtures.md §3.3 重新固化）
执行:
  - 离线: fake_llm + fake_embedder（契约见 spec/04-fixtures.md §7）
  - 在线: RUN_LLM_TESTS=1 环境变量门控
分层依据: spec/05-boundaries.md §11
```

## 4. Phase 2 — 互惠求解接入（2 周）

### 4.1 `match.py` — NSW 求解 + envy
```
输入: PrefMatrix, matching_config, blending_config, reference_scores
输出: (list[Edge], match_prob, envy_report)
功能:
  - NSW 全局互惠最优（几何平均双向偏好）
  - envy 公平性检查（own-best 语义）
  - 度约束: b_max 绑定 member 侧
  - pool_b_max 可选绑定 pool 侧
  - 未打分候选用 embedding-only 权重兜底
依赖: 纯 numpy（确定性贪心 b-matching + 集合化 envy，无 FairRec/cvxpy/torch）
参考: FairRec src/alternate_fw.py nsw_maximize + src/market.py check_envy（语义对齐）
```

### 4.2 `pre_matrix`（在 `score.py` 内实现）
```
输入: pair_scores dict, all_user_ids
输出: PrefMatrix
功能:
  - llm_score_a_to_b → pref_left_to_right[i,j]
  - llm_score_b_to_a → pref_right_to_left[j,i]
  - 缺失分数用 embed_score 兜底
```

### 4.3 `tests/test_oracles.py`
```
测试:
  - test_hr_at_n: 已知 ground-truth → 验证 HR@1/3/5 计算
  - test_ndcg_at_5: 验证 NDCG 公式
  - test_envy_free: 合成市场 → envy = 0
  - test_gate_passes: 构造通过/不通过门禁的场景
```

### 4.4 `evaluate.py` — Oracle 计算
```
输入: predictions: list[list[str]], ground_truth: list[str], pref_matrix: PrefMatrix | None, match_prob: np.ndarray | None
输出: EvaluationReport
功能:
  - HR@1/3/5、NDCG@5（AgentRecBench calculate_hr_at_n 模式）
  - envy 计数（FairRec check_envy 模式）
  - passes_gates: 按 config evaluation.gates 判定
公开签名: evaluate(predictions, ground_truth, pref_matrix, match_prob) -> EvaluationReport
```

## 5. Phase 3 — 评测闭环 + LLM 自改进（2-3 周）

### 5.1 评测循环（离线部分已完成）
```
复用 AgentRecBench Simulator 模式:
  - set_task_and_groundtruth(task_dir, groundtruth_dir)
  - set_agent(MyAgent)
  - run_simulation() → predictions
  - evaluate() → EvaluationReport

映射到互惠:
  - 经典互惠: 稳定双向偏好          → data/bench/classic.json（10×8，含竞争者）
  - 兴趣演化: 一方需求随时间变      → data/bench/drift.json（t2 漂移标注）
  - 冷启动: 新实体无历史            → data/bench/cold.json（embedding_only）

已实现（离线，确定性）:
  - surrogate.py: LLM/embedder 确定性替身（needs↔skills 重叠 + 固定 seed 噪声）
  - bench.py: run_scenario/run_scenarios/run_suite/aggregate_reports
    （推荐列表源自求解器输出 → 求解器退化直接压低 HR/NDCG，
    TestGateDiscrimination 回归守护）
  - cli.py evaluate: 三场景 + 合成市场 → 聚合门禁（CI 阻断）

待真实凭据（OPENAI_API_KEY + RUN_LLM_TESTS=1）:
  - tests/test_llm_online.py: 真实打分/embedding 链路验证（已写好，无 key 跳过）
```

### 5.2 反馈注入（已实现：feedback.py）
```
三种方式:
  1. Prompt 校准: 历史 HR/NDCG → few-shot   → feedback.calibrate_prompt ✅
  2. 权重校准: 调整 blend.embed_weight / llm_weight → feedback.calibrate_weights ✅
     （HR 下降触发，有界步进 [0.1, 0.9]，重归一化）
  3. Agent 记忆: 记录接受/拒绝的匹配       → feedback.MatchMemory ✅
     （JSONL 持久化，rejected_pair_ids 可并入 novelty 排除）
CLI: python -m mutual.cli calibrate --history reports.json
```

## 6. CI 门禁

| 门禁 | 触发时机 | 失败行为 |
|---|---|---|
| `ruff check` | 每次 push | 阻断 |
| `mypy src/` | 每次 push | 阻断 |
| `pytest tests/ -m "not llm"` | 每次 push | 阻断 |
| golden test 逐位通过 | 每次 push | 阻断 |
| 评测门禁 `HR@3≥0.6, NDCG@5≥0.4, envy≤2` | Phase 2+ 每次 push | 阻断 |
| `RUN_LLM_TESTS=1 pytest` | 手动/每周 | 报告不阻断 |

## 7. 依赖引入计划

| Phase | 依赖 | 用途 |
|---|---|---|
| 0 | numpy, pyyaml, pytest | 基础 |
| 1 | openai | LLM API |
| 2 | 无新增（纯 numpy） | NSW 求解改用轻量确定性贪心 b-matching + envy，替代 FairRec/cvxpy/torch（见 §4.1） |
| 3 | 无新增 | 复用已有 |

## 8. 验收标准

| 阶段 | 验收 |
|---|---|
| Phase 0 | `pytest tests/test_schemas.py tests/test_stages.py` 通过 |
| Phase 1 | `pytest tests/ -m "not llm"` 全部通过；golden test 逐位通过 |
| Phase 2 | `pytest tests/` 全部通过；评测门禁通过 |
| Phase 3 | 互惠 bench 三场景跑通；LLM 自改进反馈闭环演示 |
