# 01-Schemas：数据契约 Spec

> 每个数据结构是一个 dataclass，配 `to_dict` / `from_dict`。
> 选 dataclass 而非 pydantic：依赖轻，外部 caller 不需要运行时验证。
> 如果未来需要运行时验证，再加 pydantic adapter，不改 dataclass 本身。

## 数据流总览

```
Profile → ExtractedSections → HydeDescriptors → EmbeddingsBundle
  → SimilarityResult → [CandidatePair] → PairScore → PrefMatrix
  → [Edge] → Introduction → MatchResult → EvaluationReport
```

---

## 1. Profile

用户/实体的原始自由文本画像。

| 字段 | 类型 | 语义 |
|---|---|---|
| `id` | `str` | 唯一标识（uuid 或 slug） |
| `sections` | `dict[str, str]` | 自由文本分节：`{skills, vision, project, needs}` |
| `last_updated_at` | `str \| None` | ISO-8601 时间戳，adapter 层的新鲜度信号 |

**边界**：sections 的 key 不强制固定四项，缺失的 section 按"中性"处理（见 `05-boundaries.md` §1）。

## 2. ExtractedSections

Profile 经 LLM 提取后的结构化分节。

| 字段 | 类型 | 语义 |
|---|---|---|
| `id` | `str` | 与 Profile.id 对齐 |
| `sections` | `dict[str, str]` | 提取后的分节文本 |
| `hash` | `str` | sections 内容的 hash（`utils.hash_text`），驱动 embedding 复用 |

**边界**：提取失败的 section 填 `"Not specified"`，pipeline 继续运行，但 adapter **不得持久化**失败结果。

## 3. HydeDescriptors

Hypothetical Document Embeddings：为每个 section 生成假设性描述，增强 embedding 语义。

| 字段 | 类型 | 语义 |
|---|---|---|
| `id` | `str` | 用户 ID |
| `descriptors` | `dict[str, list[str]]` | `{section: [desc1, desc2, ...]}`，`n_descriptors` 可配 |

## 4. EmbeddingsBundle

所有用户的 embedding 打包，content-hash 驱动增量复用。

| 字段 | 类型 | 语义 |
|---|---|---|
| `user_ids` | `list[str]` | 用户 ID 数组（embedding 的 axis-0） |
| `section_names` | `list[str]` | section 名称数组（axis-1） |
| `embeddings` | `np.ndarray` | `[N, sections, D]` 全尺寸向量 |
| `hyde` | `dict[str, np.ndarray]` | `{section: [N, n_desc, D]}` HyDE 向量 |
| `embedding_model` | `str` | 模型标识（迁移守卫） |
| `dim` | `int` | 原始维度（MRL 截断在计算时做） |
| `section_hashes` | `dict[str, str]` | 每个 user×section 的内容 hash |
| `hyde_hashes` | `dict[str, str]` | 每个 user×section×desc 的内容 hash |
| `user_timestamps` | `dict[str, str]` | 每用户的 `last_updated_at`（新鲜度信号） |

**关键语义**：
- embedding 复用是 **content-addressed**：只重嵌内容 hash 变化的 cell。
- 不同 `embedding_model` 的 bundle 整体忽略。
- 全尺寸向量始终存储；MRL 截断在工作副本上做。

## 5. SimilarityResult

召回层的输出：方向性相似度矩阵。

| 字段 | 类型 | 语义 |
|---|---|---|
| `source_ids` | `list[str]` | 源侧 ID（M×N 模式中的 M members） |
| `target_ids` | `list[str]` | 目标侧 ID（M×N 模式中的 N pool） |
| `dir_matrix` | `np.ndarray` | `[M, N]` 方向性相似度（source→target） |
| `fused_matrix` | `np.ndarray` | `[M, N]` 跨 section 融合后的相似度 |

**关键语义**：
- **方向性不盲目对称化**：`dir_matrix[i,j]` ≠ `dir_matrix[j,i]`。
- **缺失 section = 中性**：空向量被 mask，分母只算实际存在的权重。
- N×N 方阵模式是 M×N 的特例（source = target）。

## 6. CandidatePair

从相似度矩阵中选出的、进入 LLM 精排的候选对。

| 字段 | 类型 | 语义 |
|---|---|---|
| `user1` | `str` | 字典序较小的用户 |
| `user2` | `str` | 字典序较大的用户 |
| `pair_id` | `str` | `stable_pair_id(user1, user2)` |
| `similarity_score` | `float` | embedding 相似度（选择依据） |

## 7. PairScore

LLM 精排后的双向打分结果。

| 字段 | 类型 | 语义 |
|---|---|---|
| `pair_id` | `str` | 稳定对 ID |
| `user1` | `str` | |
| `user2` | `str` | |
| `embed_score` | `float` | embedding 相似度（归一化后） |
| `llm_score` | `float \| None` | LLM 融合打分（None = 未打分，预算耗尽） |
| `llm_score_a_to_b` | `float \| None` | 方向性：user1→user2 的 LLM 分 |
| `llm_score_b_to_a` | `float \| None` | 方向性：user2→user1 的 LLM 分 |
| `embed_score_normalized` | `float \| None` | 跨批次稳定归一化后的 embedding 分 |
| `llm_score_normalized` | `float \| None` | 跨批次稳定归一化后的 LLM 分 |

**边界**：未打分候选保留 embedding-only 权重，**不静默丢弃**。

## 8. PrefMatrix（新增 — 互惠核心）

双向偏好矩阵，作为匹配市场的输入。

| 字段 | 类型 | 语义 |
|---|---|---|
| `left_ids` | `list[str]` | 左侧实体 ID |
| `right_ids` | `list[str]` | 右侧实体 ID |
| `pref_left_to_right` | `np.ndarray` | `[M, N]` 左→右偏好矩阵 |
| `pref_right_to_left` | `np.ndarray` | `[N, M]` 右→左偏好矩阵 |

**来源**：由 PairScore 的 `llm_score_a_to_b` / `llm_score_b_to_a` 填充。
**消费方**：FairRec 的 `nsw_maximize` 或 `sw_maximize`。

## 9. Edge

最终匹配边。

| 字段 | 类型 | 语义 |
|---|---|---|
| `user1` | `str` | |
| `user2` | `str` | |
| `pair_id` | `str` | |
| `final_weight` | `float` | 混合权重（embed + llm） |
| `embed_score` | `float` | |
| `llm_score` | `float` | |
| `embed_score_normalized` | `float \| None` | |
| `llm_score_normalized` | `float \| None` | |
| `intro` | `str` | 对接话术 |
| `starter_topics` | `str` | 破冰话题 |

## 10. MatchResult

一次匹配运行的完整输出。

| 字段 | 类型 | 语义 |
|---|---|---|
| `edges` | `list[Edge]` | 最终匹配边列表 |
| `report_data` | `dict` | 用户报告 + 群组摘要 |
| `new_pairs` | `list[dict]` | 本次新暴露的对（用于 history append） |
| `envy_report` | `dict \| None` | 公平性报告（来自 FairRec `check_envy`） |

## 11. EvaluationReport

评测结果，作为 LLM 自我改进的反馈。

| 字段 | 类型 | 语义 |
|---|---|---|
| `hr_at_1` | `float` | Hit Rate @1 |
| `hr_at_3` | `float` | Hit Rate @3 |
| `hr_at_5` | `float` | Hit Rate @5 |
| `ndcg_at_5` | `float` | NDCG@5 |
| `envy_count_left` | `int` | 左侧嫉妒数 |
| `envy_count_right` | `int` | 右侧嫉妒数 |
| `total_scenarios` | `int` | 评测场景总数 |
| `metadata` | `dict` | 额外信息（模型、配置 hash 等） |
