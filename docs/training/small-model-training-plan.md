# mutual 高价值小模型训练方案（开源生态尽调版）

> 目标：仅使用**合成数据 + 公开数据**，为 mutual 引擎训练高价值小模型，替代/增强关键 LLM 通路。
> 前提：不考虑真实业务反馈数据；不涉及前端。
> 本文档回答三个问题：① 开源生态里有没有现成的？② 没有/不完全符合的是哪些？③ 具体怎么训、怎么验。

---

## 0. 结论速览（先看这个）

| 候选小模型 | 替代的通路 | 开源现状判定 | 结论 | 优先级 |
|---|---|---|---|---|
| **Score 双向打分器**（cross-encoder） | score 打分（成本最大头） | **开源有，但不完全符合，需微调** | 用 bge-reranker-v2-m3 微调 | 🔴 P0 |
| **领域 Embedding 微调** | 召回层（voyage-3-lite 替换） | **开源很符合，多个备选** | 直接实测 BGE-M3/GTE，无需训练或仅轻量微调 | 🟠 P1 |
| **Extract 四节结构化器** | extract 通路（线性成本） | **开源无专用模型，但可用小 LLM 微调** | Qwen2.5-0.5B/1.5B LoRA 微调 | 🟡 P2 |
| **Introduce 话术生成器** | introduce 通路（有模板兜底） | **开源有生成模型，可微调** | Qwen2.5-1.5B LoRA 微调（或延后） | ⚪ P3 |
| **HyDE 描述器** | hyde 通路（增强召回） | 开源无专用；且若 embedding 做得好则边际价值下降 | **不建议训练** | ❌ |

---

## 1. Score 双向打分器 —— P0（成本最大头 + 攻击面最多 + 判别式最适配）

### 1.1 开源现状尽调

**判定：开源已有 cross-encoder / reranker 生态，但它们解决的是「单向相关性/相似度排序」，不完全符合 mutual 的双向互补打分（a_to_b 与 b_to_a 语义不同）。需要微调。**

开源备选池（全部可商用）：

| 模型 | 参数量 | 中文基准（CMTEB-R） | 特点 | License |
|---|---|---|---|---|
| **bge-reranker-v2-m3** | ~560M | 72.16 | 多语言 100+，M3 骨干，生态成熟（FlagEmbedding 官方支持微调） | MIT（可商用） |
| **Qwen3-Reranker-0.6B** | 0.6B | 71.31 | 中文强，32K 上下文，两阶段训练架构 | Apache 2.0 |
| bge-reranker-large | 560M | — | 中英文 cross-encoder，XLM-RoBERTa-Large | 可商用免费 |
| bge-reranker-base | 278M | — | 轻量，高并发/边缘 | MIT |
| gte-multilingual-reranker-base | 0.3B | 74.08 | 阿里，英文为主、多语言 | 需查证 |
| Qwen3-Reranker-4B | 4B | 75.94 | 更高精度，部署成本高 | Apache 2.0 |

**关键差异点（为什么不能直接用）**：
1. **单向 vs 双向**：开源 reranker 输出一个相关度分数；mutual 需要 `a_to_b`（A 需要什么 × B 能提供什么）与 `b_to_a` 两个方向分，二者语义不同（互补匹配 ≠ 相似度匹配）。
2. **画像对 vs 检索对**：mutual 输入是两段等权画像（四节结构），不是 query-document 检索对。
3. **硬约束语义**：现有 reranker 没有「声明硬约束 + 资格排除」的语义裁决能力（#63/#41/#49 相关）。

### 1.2 选型建议

- **首选微调基座：`bge-reranker-v2-m3`**（FlagEmbedding 官方 `llm_reranker` 训练管线支持 `{"query","pos","neg","prompt"}` 格式，且是 cross-encoder，直接输出 0-1 分）。
- **对比候选：`Qwen3-Reranker-0.6B`**（中文指令理解更强，天然支持指令式打分）。
- **实测方案（P0 第一步）**：两个都拉下来，在 bench 全量 + holdout 陷阱上跑一轮 zero-shot 打分，对比 LLM 基线，看哪个离可用更近。

### 1.3 训练数据（合成 + 公开）

**合成数据（仓库已有，主来源）**：
- `data/bench/`（classic/cold/drift）ground_truth 黄金对 → 正样本；非黄金对 → 负样本
- `data/bench-extended/`（paraphrase/decoy/messy/constraints/zh_assoc）→ 陷阱分布负样本 + 中文协会场景
- `holdout/scenarios/`（HT-01..12）→ 仅作测试，**禁止进训练集**
- `lab/` 生成器（30 基词×17 变体）→ 可重跑生成数万画像对，label 用 v3 契约打分离线标注
- **Label 生成**：用 LongCat-2.0（或当前 v3 契约）对合成画像对离线标注 `a_to_b`/`b_to_a`，做成回归目标（0-1 连续），或降级为「黄金对=1 / 干扰对=0」分类目标再蒸馏

**公开数据（预测练/迁移，弥补中文语义盲区）**：
- LCQMC（238,766 训练对）、BQ（~10 万）、ATEC（~10 万）→ 通用中文语义匹配
- STS-B-Chinese（5,231）→ 连续分回归
- PAWS-X 中文（49,401）→ 高词汇重叠对抗样本（对齐 decoy 陷阱）

### 1.4 训练与评测协议

```
数据：
  train: bench 全量 + bench-extended 全量 + lab 生成器重跑 N 对（比例建议 6:2:2 划分，
         严格按场景隔离，避免同源画像泄漏）
  eval:  holdout HT-01..12 + lab 8 份盲标注 624 对双向分（人工标注的独立评估集）
训练：
  用 FlagEmbedding llm_reranker 官方训练脚本，基座 bge-reranker-v2-m3
  （格式：{"query": "A画像四节", "pos": ["B画像"], "neg": [...], "prompt": "双向互补打分指令"}）
评测门禁（与引擎 redteam 门禁对齐）：
  - HR@3 ≥ 0.6（与现有 v3 LLM 持平）
  - NDCG@5 ≥ 0.4
  - total_envy ≤ 2
  - 12 陷阱集全绿（防堆砌/注入）
  - 与 v3 LLM 打分做一致性对比（Pearson/Spearman on 624 对）
```

### 1.5 接入方式

- 引擎 `engine.LLMClient` 是四方法接口（`llm.go`），score 通路走 `CompleteScore`。
- 训练好的 cross-encoder 封装为一个 `RerankerScoreClient`，实现 `CompleteScore`，**pipeline 零改动**。
- 输出连续分 → 进入现有 `PrepareNormalizedScores`（注意 #70/#44 归一化死代码需先处理）。

---

## 2. 领域 Embedding 微调 —— P1（召回层质量上限）

### 2.1 开源现状尽调

**判定：开源很符合，多个备选，直接实测即可；大概率不需要训练。**

| 模型 | 参数量 | 维度 | 中文 | License |
|---|---|---|---|---|
| **BGE-M3**（智源） | 567M | 1024 | 中文第一、100+ 语言、稠密+稀疏+多向量 | Apache 2.0（可商用） |
| **GTE**（阿里通义） | 多档 | 1024 | 中文强、多任务 | 需查证 |
| bge-large-zh-v1.5 | 335M | 1024 | 中文知识库/FAQ | MIT |
| Youtu-Embedding（腾讯） | — | — | 通用文本表示六大任务 | 开源（2025-10） |
| text2vec-base-chinese | ~110M | 768 | 中文匹配、Apache 2.0 可商用 | Apache 2.0 |
| ⚠️ m3e-base | ~110M | 768 | **非商用（研究用途）** | **不能用** |

### 2.2 建议

1. **不训练优先**：直接用 BGE-M3 替换 `voyage-3-lite`，跑 bench 全量 + holdout，对比 HR 变化。BGE-M3 支持稠密+稀疏混合，召回质量预期高于通用 voyage。
2. **仅当替换后 HR 仍不达标，才考虑轻量微调**：
   - 基座：BGE-M3（FlagEmbedding 支持 `unified_finetuning`）
   - 数据：合成黄金对 = 正样本，干扰对/陷阱对 = 负样本（对比学习三元组天然具备）
   - 注意：**embedding 变更会改 golden 对拍结果（逐位），属于 spec 语义变更，必须走 ADR 评审**——这是它排 P1 而非 P0 的原因。

---

## 3. Extract 四节结构化器 —— P2（线性成本，可延后）

### 3.1 开源现状尽调

**判定：开源没有「画像→四节」的专用模型，但有成熟的「小 LLM + LoRA 微调做信息抽取」方案。可用 Qwen2.5 系微调。**

- Qwen2.5-0.5B/1.5B/3B-Instruct + LLaMA-Factory LoRA：社区有大量「0.5B/1.5B 微调做信息抽取/分类」成熟教程与踩坑记录。
- 参考任务：简历→结构化、JD 分类（公开有 QLoRA Qwen2.5-0.5B 做 JD 分类的完整实现）。
- 公开语料：480 万企业名称语料库（Company-Names-Corpus）、35 万企业实体数据集（工商公示）、CompanyKG（117 万公司+描述，可借鉴英文抽取；中文主要靠合成）。

### 3.2 建议

- 训练目标：把四节抽取从「每画像 1 次 LLM 调用」变成「本地小模型调用」，**消除 extract 提示词注入面（#52/#46）**。
- 基座选 Qwen2.5-1.5B-Instruct（中文抽取能力>0.5B，LoRA 训练单卡可行）；若延迟敏感可降 0.5B。
- 数据：lab 生成器产出画像文本，配对的真实分节就是监督目标（合成数据天然自带标签）。
- **不优先的原因**：extract 是线性成本（每画像 1 次），score 才是 4800 次；且当前 LLM extract 质量已够用，小模型替换的边际收益有限。

---

## 4. Introduce 话术生成器 —— P3（可延后，等真实反馈）

### 4.1 开源现状尽调

**判定：开源有中文生成小模型，可微调；但 introduce 有 `AttachFallbackIntro` 模板兜底，失败不阻塞核心。**

- Qwen2.5-0.5B/1.5B-Instruct、ChatGLM4-0.5B、Qwen3 系等中文小生成模型均开源可商用。
- 训练任务：匹配对 → 双向对接话术 + 破冰话题（生成式）。

### 4.2 建议

- **现在不做**。理由：话术质量不影响匹配正确性；小模型生成话术与 LLM 差距明显；等业务跑起来积累真实反馈后再考虑。
- 若做：Qwen2.5-1.5B LoRA 微调，数据用合成匹配对 + LLM 生成多样话术作 label。

---

## 5. 不建议训练：HyDE 描述器

- HyDE 的目的就是增强 embedding 召回；若 P1 的 BGE-M3 替换/微调做好，HyDE 边际价值直接下降。
- 且 #68 已证 HyDE max-pool 有单候选饱和问题。
- **不投入训练资源。**

---

## 6. 数据资产与合规清单

### 6.1 合成数据（仓库已有，零成本）

| 资产 | 用途 | 是否可入训练 |
|---|---|---|
| `data/bench/*`（classic/cold/drift） | 黄金对正/负样本 | ✅ |
| `data/bench-extended/*`（含 zh_assoc 中文协会） | 陷阱分布负样本 + 中文场景 | ✅ |
| `holdout/scenarios/*`（HT-01..12） | 人工陷阱测试集 | ❌ 仅测试 |
| `lab/` 生成器（未入库可重跑） | 大规模画像对生成 + 双向分标注 | ✅ 需重跑 |
| 8 份盲标注 624 对双向分 | 独立人工评估集 | ✅ 仅评估 |

### 6.2 公开数据（需下载，注意 license）

| 数据集 | 规模 | 用途 | License 注意 |
|---|---|---|---|
| LCQMC | 238,766 对 | 中文语义匹配预测练 | 可研究 |
| BQ / ATEC | ~10 万对 | 领域判别迁移 | 可研究 |
| PAWS-X 中文 | 49,401 对 | 高重叠对抗样本 | 可研究 |
| STS-B-Chinese | 5,231 对 | 连续分回归 | 可研究 |
| C-MTEB | 评测集 | embedding 评测基准 | — |
| CompanyKG | 117 万公司 | 英文相似度借鉴 | 可研究 |
| 480 万企业名称语料 | 语料 | extract 分词/实体 | 需查证商用条款 |

**⚠️ 合规红线**：
- `m3e-base` **非商用**，禁止用于生产，仅研究对比可。
- 公开数据集多数标注「研究用途」，若最终模型要商用部署，需逐一核对 license 或改用 Apache 2.0 系（BGE/Qwen/text2vec 可商用）。
- 合成数据（lab/bench）是自产，license 无约束。

---

## 7. 分阶段执行路线图

### 阶段 0（立即，0.5-1 周）：Score 实测
- [ ] 拉取 bge-reranker-v2-m3 与 Qwen3-Reranker-0.6B
- [ ] 在 bench 全量 + holdout 上 zero-shot 打分，与 v3 LLM 基线对比 HR/NDCG/envy
- [ ] 产出对比报告 → 定基座

### 阶段 1（P0，2-3 周）：Score 微调
- [ ] 合成数据重跑 lab 生成器 → 生成训练画像对
- [ ] LongCat-2.0 / v3 契约离线标注双向分（或黄金对分类）
- [ ] FlagEmbedding llm_reranker 微调（先过 12 陷阱 + 624 对评估门禁）
- [ ] 封装 `RerankerScoreClient` 接入 `CompleteScore`，跑 golden 对拍
- [ ] **前置处理 #70/#44 归一化死代码**

### 阶段 2（P1，1-2 周）：Embedding 替换实测
- [ ] BGE-M3 替换 voyage-3-lite，bench 对比 HR
- [ ] 若提升显著 → 走 ADR 评审更新 golden；若不达标 → 轻量对比学习微调

### 阶段 3（P2/P3，可选）：Extract / Introduce
- [ ] Qwen2.5-1.5B LoRA 微调 extract 四节结构化
- [ ] introduce 延后

---

## 8. 关键风险与开放问题

1. **合成数据 → 真实数据的泛化**：合成画像有「唯一 aspect 槽」可辨认性，真实协会画像信号弱得多。Score 微调后必须用真实画像（哪怕 20-50 条脱敏人工标注）做一次端到端验证，否则会重蹈「合成饱和 ≠ 真实可用」覆辙。
2. **golden 对拍破坏**：任何模型替换（尤其 embedding）都会改 golden 逐位结果，需 ADR 评审流程，不允许 hack 测试。
3. **双向语义**：开源 reranker 的「相关度」≠ mutual 的「互补匹配」；微调时必须用双向 label，避免模型学到「相似度高=分高」的偏置（这与 mutual 的互补匹配哲学冲突）。
4. **商用合规**：若 mutual 最终用于协会/商会商业服务，公开数据 license 逐一核查，首选 Apache 2.0/MIT 系基座。

---

*本文档基于 2026-08 开源生态尽调（reranker/embedding/小 LLM 生态公开资料），数据规模与指标来自公开 benchmark 报道。*
