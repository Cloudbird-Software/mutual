# mutual 训练可借鉴开源项目清单

> 本文档是训练工程（`scripts/training/`）的"开源弹药库"：每个项目给出**借鉴点**（直接抄哪块能提升效果）与**接入方式**。全部为开源可商用（除标注）。
> 维护：新借鉴项须附 license 与用途，禁止 AGPL/GPL-3.0/SSPL。

---

## 1. Score 打分器（P0）——核心借鉴

### 1.1 FlagEmbedding（BAAI/智源）——**首选训练管线**

- 仓库：https://github.com/FlagOpen/FlagEmbedding
- License：MIT（bge-reranker-v2-m3 可商用）
- **借鉴点**：
  - `research/llm_reranker/` 训练管线：`{"query", "pos", "neg", "prompt"}` 数据格式 + 两阶段训练（比对阶段 + 指令跟随阶段），是"reranker 指令化"的成熟实现；
  - `unified_finetuning` / `use_self_distill`（自蒸馏）：**用小模型蒸馏大 LLM 的排序能力**——可直接用于"用 v3 LLM 或 LongCat 标注合成数据，再蒸馏到小 reranker"；
  - 多向量 / 稠密+稀疏混合的评测协议。
- 接入：训练脚本基于它的数据格式；`bge-reranker-v2-m3` 直接作为基座权重。

### 1.2 sentence-transformers（UKP/HuggingFace）——训练框架

- 仓库：https://github.com/UKPLab/sentence-transformers
- License：Apache 2.0
- **借鉴点**：
  - `CrossEncoder` 训练（`cross_encoder/training_overview`）：加载预训练 transformers + 序列分类头，支持回归 loss——正是双向打分器的骨架；
  - `RerankEvaluator`：训练中周期性评估，避免过拟合；
  - hard negatives 挖掘流程（`example/training/...`）：用 embedding 检索挖难负样本——可对合成负样本做难例挖掘提升训练效率。
- 接入：`train_reranker.py` 的框架层。

### 1.3 Qwen3-Reranker（阿里）——对比候选

- 仓库：https://github.com/QwenLM/Qwen3  （embedding/reranker 分支）；博客 https://qwenlm.github.io/zh/blog/qwen3-embedding/
- License：Apache 2.0
- **借鉴点**：
  - `Qwen3-Reranker-0.6B`：中文指令理解更强，与 bge-reranker-v2-m3 做 A/B 实测对比（`TRAINING_SPEC §1.3`）；
  - 两阶段训练架构（通用预训练 → 指令微调）可迁移到 mutual 的"通用语义 → 双向互补指令"。
- 接入：直接作为基座，或对比评测。

### 1.4 BGE-Reranker 家族（模型本身）

- ModelScope/HF：`BAAI/bge-reranker-base`(278M) / `large`(560M) / `v2-m3`(560M)
- License：MIT
- **借鉴点**：v2-m3 的 XLM-RoBERTa 骨干天然支持中英混合；base 版本适合延迟敏感场景。实测时三档都跑一下 latency/质量权衡。

---

## 2. Embedding 替换/微调（P1）

### 2.1 BGE-M3（智源）——首选替换

- 仓库：https://github.com/FlagOpen/FlagEmbedding （`research/BGE_M3/`）
- License：Apache 2.0
- **借鉴点**：稠密+稀疏+多向量三合一；自知识蒸馏训练法（`Self-Knowledge Distillation`）——**若需微调，用它的 m3_kd_loss 在黄金对/干扰对上做对比学习**，比普通 InfoNCE 更稳。
- 接入：`bench_embedding.py` 直接测；维度 1024（引擎 `embedding_dimensions` 支持 MRL）。

### 2.2 GTE（阿里通义）——对比候选

- 仓库：https://github.com/Alibaba-NLP/gte-multilingual
- License：需查证（Apache 2.0 倾向）
- **借鉴点**：`gte-multilingual-base` 小体积 + 中文强；弹性稠密向量。

### 2.3 text2vec（shibing624）——轻量中文基线

- 仓库：https://github.com/shibing624/text2vec
- License：Apache 2.0（可商用）
- **借鉴点**：`text2vec-base-chinese-paraphrase`（CoSENT 训练，ERNIE-3.0 骨干）——轻量中文匹配基线，可用于 embedding 对比的下限参照；`nli-zh-all` 中文 NLI 语料可作为对比学习辅助数据。
- ⚠️ 兄弟模型 `m3e-base` **非商用，禁止**。

### 2.4 C-MTEB / C-MTP（智源）——评测与预测练语料

- 仓库：https://github.com/FlagOpen/FlagEmbedding （`research/C_MTEB/`）；C-MTP 语料见 BGE 论文
- License：评测数据研究可用
- **借鉴点**：C-MTEB 中文检索/相似度基准 = embedding 效果的标准评测面；C-MTP 百万级训练语料可做领域外预测练。

---

## 3. Extract 四节结构化（P2）

### 3.1 LLaMA-Factory（hiyouga）——微调框架

- 仓库：https://github.com/hiyouga/LLaMA-Factory
- License：Apache 2.0
- **借鉴点**：LoRA/QLoRA 一键微调 Qwen2.5 做信息抽取（社区大量"0.5B 微调做 JD 分类/信息抽取"成熟教程）；`llamafactory-cli train` 配置文件化，适合 PM 直接跑。
- 接入：`train_extract.py` 生成 LLaMA-Factory 的 dataset 格式（`instruction/input/output`），或直接用其 CLI。

### 3.2 Qwen2.5（阿里）——基座模型

- 仓库：https://github.com/QwenLM/Qwen2.5
- License：Apache 2.0
- **借鉴点**：`Qwen2.5-0.5B/1.5B-Instruct` 中文信息抽取 + JSON 输出能力；0.5B 可单卡 8GB LoRA 训练。

### 3.3 CompanyKG（新加坡管理大学）——英文企业图谱借鉴

- 仓库：https://github.com/HPI-Information-Systems/CompanyKG
- License：CC BY-NC 4.0（**非商用**，仅借鉴方法）
- **借鉴点**：117 万公司节点 + 15 种关系 + 公司相似度三项评测任务——是"企业级匹配"的学术参照，可借鉴其**相似度/竞争关系标注协议**来设计 mutual 的画像匹配评测；数据本身中文侧不可直接用。

---

## 4. 互惠推荐学术借鉴（RRS）

### 4.1 RecBole / RecBole-PJF（北大）——互惠/人岗匹配基线

- 仓库：https://github.com/RUCAIBox/RecBole
- License：MIT
- **借鉴点**：`RecBole-PJF`（Person-Job Fit）是"人岗双向匹配"的完整基线库，含多任务损失（双侧拟合）；mutual 的双向打分与其 JobFit 任务同构——**可用其损失设计（双侧 + 交叉熵）参考**，但其基于离散 ID 交互，与 mutual 的文本画像不同，需适配。
- 注意：这是学术基线，不直接替代 cross-encoder；价值在**损失函数与评测协议**。

### 4.2 OpenOneRec（快手）——生成式推荐 LLM

- 仓库：https://github.com/Kuaishou-OneRec/OpenOneRec
- License：需查证（Apache 2.0 倾向）
- **借鉴点**：Qwen 架构生成式推荐（1.7B/8B），`RecIF-Bench` 指令遵循基准——若未来把 score 升级为"生成式评分 + 理由"，可借鉴；本阶段 P0 用 cross-encoder，不引入。

### 4.3 经典 RRS 论文（仅方法）

- "User recommendation in reciprocal and bipartite social networks"（arXiv:1311.02526）
- "A Best-of-Both Approach... Reciprocal Recommendations for Job Search"（arXiv:2409.10992）
- **借鉴点**：互惠匹配的"双向互信息/双向命中率"评估口径，可支撑 mutual 的 `total_envy` 等门禁语义。

---

## 5. 中文语料（公开数据，注意 license）

| 数据集 | 规模 | 用途 | License 注意 |
|---|---|---|---|
| LCQMC | 训练 238,766 对 | 中文语义匹配预测练 | 研究可用 |
| BQ | ~10 万对 | 领域判别迁移 | 研究可用 |
| ATEC | ~10 万对 | 语义匹配 | 研究可用 |
| PAWS-X 中文 | 训练 49,401 对 | 高重叠对抗样本 | 研究可用 |
| STS-B-Chinese | 训练 5,231 对 | 连续分回归 | 研究可用 |
| Company-Names-Corpus | 480 万企业名 | extract 分词/实体 | 需查证商用条款 |
| B2B business dataset samples | 数千条 | 企业画像参考 | GitHub 公开，需查证 |

---

## 6. 快速选型表

| 需求 | 首选 | 对比 | 明确不用 |
|---|---|---|---|
| Score 打分器训练 | FlagEmbedding + bge-reranker-v2-m3 | Qwen3-Reranker-0.6B | — |
| Embedding 替换 | bge-m3 | gte-multilingual-base | m3e-base（非商用） |
| Extract 微调 | LLaMA-Factory + Qwen2.5-1.5B | Qwen2.5-0.5B | — |
| 双向损失借鉴 | RecBole-PJF | OpenOneRec | — |
