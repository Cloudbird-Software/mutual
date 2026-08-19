# 02-Stages：Pipeline 阶段声明

> 每个 stage 是一个 **纯变换**：输入是 plain arguments，输出是 dataclass。
> 一切 IO（文件读写、DB、API）归 adapter，core 阶段不碰存储。
> `StageSpec` 注册表让外部 caller 无需读源码即可了解每阶段的 IO 契约。

## 阶段总览

```
extract → hyde → embed → similarity → select → score → pre_matrix → match → introduce → report → evaluate
```

---

## 1. extract

| | |
|---|---|
| **输入** | `profiles: list[Profile]`, `config: dict` |
| **输出** | `list[ExtractedSections]` |
| **语义** | LLM 从自由文本画像提取结构化分节（skills/vision/project/needs） |
| **load** | `load_sections(path) → list[ExtractedSections]` |
| **dump** | `dump_sections(sections, path)` |
| **边界** | 提取失败填 "Not specified"；`extract_sections(failed_out=…)` 报告失败项 |
| **实现** | `src/mutual/extract.py`（Phase 1） |

## 2. hyde

| | |
|---|---|
| **输入** | `sections: list[ExtractedSections]`, `config: dict` |
| **输出** | `dict[str, HydeDescriptors]`（按 user_id 索引） |
| **语义** | 为每个 section 生成假设性描述，增强 embedding 语义匹配 |
| **load** | `load_hyde(path) → dict[str, HydeDescriptors]` |
| **dump** | `dump_hyde(hyde, path)` |
| **config** | `hyde.n_descriptors`（默认 1） |
| **实现** | `src/mutual/hyde.py`（Phase 1） |

## 3. embed

| | |
|---|---|
| **输入** | `sections: list[ExtractedSections]`, `hyde: dict`, `config: dict`, `existing: EmbeddingsBundle \| None` |
| **输出** | `EmbeddingsBundle` |
| **语义** | 生成 section + HyDE 向量；content-hash 驱动增量复用 |
| **load** | `load_bundle(path) → EmbeddingsBundle` |
| **dump** | `dump_bundle(bundle, path)` |
| **边界** | 不同 model 的 bundle 整体忽略；全尺寸存储，MRL 截断在计算时做 |
| **实现** | `src/mutual/embed.py`（Phase 1） |

## 4. similarity

| | |
|---|---|
| **输入** | `source: EmbeddingsBundle`, `target: EmbeddingsBundle \| None`, `recipe_config: dict` |
| **输出** | `SimilarityResult` |
| **语义** | 计算方向性相似度矩阵；`target=None` 时为 N×N 方阵模式 |
| **纯函数** | 是，无副作用 |
| **边界** | 缺失 section = 中性（mask + 分母修正）；方向性不盲目对称化 |
| **实现** | `src/mutual/similarity.py`（Phase 1）：`compute_similarity(source, target, recipe_config)` |

## 5. select

| | |
|---|---|
| **输入** | `similarity: SimilarityResult`, `budgets: dict`, `excluded_pairs: set[str] \| None` |
| **输出** | `list[CandidatePair]` |
| **语义** | 贪心轮转选择候选对，每用户有 per-profile cap，全局有 global cap |
| **边界** | 排除 history 中已暴露的 pair（novelty）；只保留正相似度对 |
| **实现** | `src/mutual/select.py`（Phase 1）：`select_pairs(similarity, budgets, excluded_pairs)` |

## 6. score

| | |
|---|---|
| **输入** | `selected_pairs: list[CandidatePair]`, `sections_dict: dict`, `instruction: str`, `prompt_template: str`, `llm_wrapper`, `config: dict` |
| **输出** | `dict[str, PairScore]` + `unscored_pairs: list[CandidatePair]`（via out-param） |
| **语义** | LLM 对候选对做 **双向** 打分（A→B 和 B→A 分别打分） |
| **边界** | 未打分候选保留 embedding 权重，不丢弃；缓存按完整 prompt hash |
| **实现** | `src/mutual/score.py`（Phase 1） |

## 7. pre_matrix（新增 — 互惠桥接）

| | |
|---|---|
| **输入** | `pair_scores: dict[str, PairScore]`, `all_user_ids: list[str]` |
| **输出** | `PrefMatrix` |
| **语义** | 把 PairScore 的方向性分数填入双向偏好矩阵 |
| **纯函数** | 是 |
| **实现** | `src/mutual/score.py`（Phase 1）：`build_pref_matrix(pair_scores, all_user_ids)` |

## 8. match

| | |
|---|---|
| **输入** | `pref_matrix: PrefMatrix`, `matching_config: dict`, `blending_config: dict`, `reference_scores: np.ndarray \| None` |
| **输出** | `(list[Edge], match_prob, envy_report)` |
| **语义** | NSW / α-SW 全局匹配求解 + envy 公平性检查 |
| **依赖** | FairRec `nsw_maximize` + `check_envy` |
| **边界** | 度约束 `b_min`/`b_max` 绑定 member 侧；`pool_b_max` 可选绑定 pool 侧 |
| **实现** | `src/mutual/match.py`（Phase 2） |

## 9. introduce

| | |
|---|---|
| **输入** | `edges: list[Edge]`, `sections_dict: dict`, `instruction: str`, `prompt_template: str`, `llm_wrapper` |
| **输出** | `dict[str, Introduction]` |
| **语义** | 为每对匹配生成双向对接话术 + 破冰话题 |
| **fallback** | LLM 失败时 `attach_fallback_intro` 生成模板话术 |
| **实现** | `src/mutual/introduce.py`（Phase 1） |

## 10. report

| | |
|---|---|
| **输入** | `edges: list[Edge]`, `extracted: list[ExtractedSections]`, `top_matches_per_user: int`, `scope_user_ids: list[str] \| None` |
| **输出** | `dict`（用户报告 + 群组摘要） |
| **语义** | 生成人类可读的匹配报告 |
| **纯函数** | 是 |
| **实现** | `src/mutual/report.py`（Phase 1）：`create_report(edges, extracted, top_matches_per_user, scope_user_ids)` |

## 11. evaluate

| | |
|---|---|
| **输入** | `predictions: list[list[str]]`, `ground_truth: list[str]`, `pref_matrix: PrefMatrix \| None`, `match_prob: np.ndarray \| None` |
| **输出** | `EvaluationReport` |
| **语义** | 计算 HR@1/3/5、NDCG@5（推荐质量）+ envy 计数（互惠公平） |
| **依赖** | AgentRecBench `calculate_hr_at_n` + FairRec `check_envy` |
| **门禁** | `hr_at_3 >= 0.6 AND envy_count_left + envy_count_right <= 2` |
| **实现** | `src/mutual/evaluate.py`（Phase 2）：`evaluate(predictions, ground_truth, pref_matrix, match_prob)` |
