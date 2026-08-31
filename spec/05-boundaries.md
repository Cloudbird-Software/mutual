# 05-Boundaries：显式边界决定

> Spec 沉默 = 没说清楚的边界。实现需要"自己猜"时，说明 spec 有沉默。
> 此时 **改 spec 而非 hack 实现**。本文件列出所有已做出的显式边界决定。

## 1. 缺失 Section = 零相似计入，不是分母豁免

**决定**：当用户的某个 section 为空或缺失时，其 embedding 为零向量，
该 term 的分子贡献为 0，但配置权重仍计入分母。融合输出再做 [0,1]
值域 clamp。

**理由**：原"mask + 分母修正"（缺失从分母豁免）的中性化只对全正
权重成立；存在负权重分节时，"留空豁免稀释 + 只填交叉对齐的惩罚
分节"可把分母压小而分子保留，融合分放大到 4/3 突破余弦值域
（红队 RT3 #54）。零相似计入让留空不再有评分优势（双侧全有效时
与旧语义逐位一致，golden 不受影响）；[0,1] clamp 兜底反相关惩罚
项的理论越界。"只填了 needs 的查询"仍可正常工作（零相似计入只是
不再相对占便宜，方向信号照常参与融合）。

**实现位置**：`similarity` stage 的融合函数。

## 2. 方向性不盲目对称化

**决定**：A→B 与 B→A 的相似度/LLM 分数分开计算和存储，不取平均。

**理由**：互惠推荐的核心是"双向一致"，但"一致"不等于"相同"。A 对 B 的需求匹配度可能高于 B 对 A。盲目对称化会丢失方向性信息。

**例外**：N×N 方阵模式的 legacy 路径会做 `(dir + dir.T) / 2` 对称化，仅为保持与旧代码的 bit-exact 兼容。新代码（M×N 模式）不做。

## 3. 未打分候选保留 Embedding 权重

**决定**：LLM 调用预算耗尽或批次失败时，未打分的候选对保留其 embedding-only 权重，参与匹配，不静默丢弃。

**理由**：丢弃会导致"热门用户耗尽预算后，其候选全部消失"的不公平。

**实现位置**：`score` stage 的 `unscored_out` 参数 + `match` stage 的兜底逻辑。

## 4. 失败抽取不持久化

**决定**：LLM 提取 section 失败时，返回 "Not specified" 让 pipeline 继续，但 adapter **不得持久化**失败结果（否则永远不会重试）。

**实现位置**：`extract` stage 的 `failed_out` 参数。

## 5. LLM 缓存按完整 Prompt Hash

**决定**：LLM 响应缓存的 key 是 **完整 prompt 的 hash**（`utils.hash_text`），不是 roster/pair_id。

**理由**：prompt 中嵌入了 profile 内容，profile 编辑后 prompt 自动变化，缓存自动失效。roster-keyed 缓存在 profile 编辑后会静默返回 stale 分数。

**禁止**：使用 Python 内置 `hash()`（进程间 salt 不同，缓存无法跨 run 命中）。

## 6. Embedding 复用是 Content-Addressed

**决定**：embedding 复用以 `section_hashes` 为准，不以 roster 为准。改一个 profile 只重嵌该 profile 的变化 cell，不影响其他人。

**理由**：roster-addressed 复用在增删用户时需要全量重嵌。

## 7. 度约束绑定 Member 侧

**决定**：`matching.b_min` / `b_max` 绑定 member 侧（"需要匹配的人"）；`pool_b_max` 可选绑定 pool 侧（防止热门用户饱和）。

**理由**：batch 模式（M×N）中，member 是主动方，pool 是候选方。约束应对称地保护 member 的匹配数。

## 8. Novelty 排除窗口

**决定**：`matching.novelty_window_months`（默认 6）内的已暴露 pair 在本次运行中排除。超过窗口的旧 pair 重新变得可匹配。

**实现位置**：adapter 从 `match_history.jsonl` 构建 `excluded_pairs`。

## 9. LLM 调用从 asyncio 进入

**决定**：所有 LLM 阶段通过 `llm.run_coro_blocking` 进入 asyncio，兼容同步代码和宿主事件循环。

**理由**：`asyncio.run` 在宿主的运行事件循环中会 raise，静默降级打分。

## 10. 评测门禁是 Spec 的一部分

**决定**：评测通过标准（`HR@3 ≥ 0.6`、`NDCG@5 ≥ 0.4`、`total_envy ≤ 2`）写入 config，CI 门禁强制执行。

**理由**：把评测从"事后验证"变成"开发门禁"，代码改版必须回归通过。

## 11. Golden 断言分层：算法无关 vs 求解器相关

**决定**：golden 断言分两层。Phase 1 只钉**算法无关不变量**：边数/度分布（由度约束推得）、方向性不对称（由 fake 分数表驱动）、确定性（同输入逐位复现）、fallback 行为。依赖 NSW 求解器的期望（`cohort.json` 的 `envy_report`、`market_30x20.json` 的 `total_matches=20 / envy=0`）随 Phase 2 求解器一起激活。`cohort.json` 中由参考实现移植的数值统计（`score_statistics.final_weights` / `embedding_scores`）不作为 Phase 1 断言，Phase 1 实现合入后按 spec/04-fixtures.md §3.3 的固化流程重新生成（走 spec 变更）。

**理由**：Phase 1 没有 NSW 求解器（FairRec 是 Phase 2 依赖）。若 Phase 1 用占位匹配器强行产出 envy=0，Phase 2 换真实求解器时必然迫使 fixture 期望值重钉——golden 会从"契约"退化为"实现快照"。评测门禁已在 docs/ci-gates.md §2.6 定为 Phase 2+ 启用，golden 分层与之对齐。

**实现位置**：`tests/test_golden.py` 的分层断言；Phase 2 激活 envy / market 断言。

## 12. Fake 的行为是 Spec 的一部分

**决定**：golden 期望值依赖 fake_llm / fake_embedder 的行为，因此 fake 的确定性契约（分数查表、路由规则、随机种子）写入 spec/04-fixtures.md §7，由 `tests/conftest.py` 实现、`tests/test_fakes.py` 守护。

**理由**：fake 行为若留在测试代码里不被 spec 固定，不同实现者会造出不同 fake，golden 的"逐位通过"失去意义；且种子必须跨进程稳定（禁用内置 `hash()`，与 §5 同源原则）。

**实现位置**：`tests/conftest.py` + `tests/test_fakes.py`。
