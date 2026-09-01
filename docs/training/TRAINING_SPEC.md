# mutual 训练规格（TRAINING_SPEC）

> 读者：训练项目经理（PM agent）与训练工程师。
> 范围：仅用**合成数据 + 公开数据**训练 mutual 的高价值小模型；真实业务反馈不在本阶段。
> 本规格是训练工作（`scripts/training/`）的验收依据——所有训练产物须满足本文档的门禁才可交付。

---

## 0. 目标与优先级

| 优先级 | 产物 | 替代通路 | 成功标准（详见 §6 门禁） |
|---|---|---|---|
| **P0** | Score 双向打分器（cross-encoder） | `score` 通路（成本最大头） | 门禁 6.1 全绿，golden 对拍语义通过 |
| **P1** | 领域 Embedding 替换/微调 | 召回层（voyage-3-lite） | 门禁 6.2 全绿，ADR 评审通过 |
| P2 | Extract 四节结构化器 | `extract` 通路 | 门禁 6.3 全绿 |
| P3 | Introduce 话术生成器 | `introduce` 通路 | **本阶段不做**（等真实反馈） |

**本阶段只做 P0（必须）+ P1（评估，若时间允许）；P2 可选。**

---

## 1. P0：Score 双向打分器

### 1.1 任务定义

输入：画像对 `(A, B)`（各自四节：skills/vision/project/needs 的自由文本）。
输出：`a_to_b`（A 从 B 获得的价值，0-1）、`b_to_a`（B 从 A 获得的价值，0-1）。

- **双向非对称**：`a_to_b` 与 `b_to_a` 独立，语义依据是"对方 skills 满足本方 needs"的互补匹配，**不是**对称相似度。不能镜像。
- 打分语义与 `baml_src/score.baml` 的 Judgment discipline 对齐（判断纪律逐条继承）：
  - 语义等价算匹配（改写/跨语言等价都算，中文 `商超铺货渠道` ↔ `ka retail entry`）；
  - 关键词回显无实质 = 不值钱；
  - **Verifiability gate**：无数字/名称/许可/可交付物支撑的宣称按"未提出"计分；
  - 硬约束优先：一方声明硬约束且对方可见违反 → 该方向 ≤0.1；
  - 阶段/规模不匹配 = 弱，不是中等；
  - 校准锚点：0-0.1 / 0.15-0.3 / 0.35-0.55 / 0.6-0.8 / 0.85-1.0。

### 1.2 数据规格

**数据源（全部合成 + 公开，无真实业务反馈）**：

| 来源 | 路径 | 用途 | 是否可入训练 |
|---|---|---|---|
| 主库 | `data/bench/{classic,cold,drift}.json` | 正/负样本 | ✅ |
| 扩展套件 | `data/bench-extended/*.json` | 陷阱分布负样本 + 中文协会（zh_assoc） | ✅ |
| holdout 陷阱 | `holdout/scenarios/HT-*.json` | **仅测试，禁入训练** | ❌ |
| 合成生成器 | `scripts/training/prepare_data.py` 内 `--synthesize` | 大规模画像对 + 双向分 | ✅ |
| 公开语义语料 | LCQMC / BQ / ATEC / PAWS-X / STS-B-Chinese | 预测练/迁移 | ✅（见 §7 合规） |

**画像序列化格式**（与引擎 `domain.Profile.ToMap` 对齐）：
```json
{
  "id": "member_1",
  "sections": {
    "skills": "...",
    "vision": "...",
    "project": "...",
    "needs": "..."
  }
}
```
训练样本序列化时四节用 `key: value` 换行拼接（与 `prompt.go FormatSections` 同构），示例：
```
skills: lag free settlement 落地经验十年 交付团队完备
vision: 金融科技长期主义深耕
project: 金融科技项目第1期，涉及lag free settlement落地
needs: 急需金融科技方向的lag free settlement能力合作伙伴
```

**样本生成**（`prepare_data.py`）：
- 正样本：`ground_truth` 黄金对（member↔pool），label 双向 = 1.0；
- 负样本：同一场景内非黄金对，label 双向 = 0.0；
- 陷阱负样本：decoy/messy/paraphrase 场景的非黄金对（paraphrase 是"改写等价"正样本但词面不同，**必须保留**以训练语义等价能力）；
- 中文样本：zh_assoc 场景全部（30×30 对）。
- 规模目标：主库 + 扩展 + zh_assoc 约 1500-3000 对；`--synthesize` 扩展至 ≥1 万对。
- **划分**：按场景隔离，`train/val/test` = 8:1:1（test 保留独立；holdout 永远不碰）。

### 1.3 模型基座与架构

- **首选基座：`BAAI/bge-reranker-v2-m3`**（~560M，XLM-RoBERTa 系，跨语言 + 中文强，MIT 可商用）。
  - FlagEmbedding `llm_reranker` 训练管线支持；或 sentence-transformers `CrossEncoder`。
- **对比候选：`Qwen/Qwen3-Reranker-0.6B`**（Apache 2.0，中文指令理解更强）。
- **双向输出实现（二选一，推荐 B）**：
  - A) 单模型 + 方向前缀：输入 `[方向指令] A画像 [SEP] B画像`，用方向前缀区分 `a_to_b` / `b_to_a`（一个模型输出单分，训练时同一对生成两个方向样本）。
  - B) 双输出头：`CrossEncoder` 定制回归头输出两个分数（简单、直观）。
- **基线必须记录**：训练前先跑 `python evaluate_reranker.py --model BAAI/bge-reranker-v2-m3 --data ./output/data --out ./output/data/baseline.json`（zero-shot，注意 `--data` 为必填）作为对照，训练后对比提升。

### 1.4 训练参数（起点，训练者按数据量调整并记录）

```
框架：sentence-transformers CrossEncoder（脚本实际实现，train_reranker.py）
loss：BinaryCrossEntropyLoss（label=黄金对1/非黄金0；如改回归连续分请换 MSE 系 loss）
epochs: 3-5（脚本默认 4）
batch_size: 16-32（显存不足降梯度累积；脚本默认 16）
lr: 2e-5（脚本默认）
max_seq_len: 512（四节拼接）
评估器：训练后由 evaluate_reranker.py 跑门禁（脚本内未挂每 epoch evaluator）
```

> ⚠️ **训练环境提醒**：训练脚本（train_reranker.py / evaluate_reranker.py）依赖
> torch / sentence-transformers 等大依赖，**仅需在 PM agent 的 GPU 环境安装**（`pip install -r requirements.txt`）。
> 本仓库提交时已通过 Python 语法检查（py_compile）与 prepare_data.py 数据管线实跑验证，
> **但训练/评测本身未在无 GPU 的开发机实跑**——若遇 sentence-transformers 版本 API 差异
> （如 fit 签名、loss 命名），以本节的 loss/参数语义为准调整，并在训练报告中记录改动。
```

### 1.5 交付物

1. 模型权重目录（含 config + tokenizer + 训练/评测日志）；
2. 评测报告 `evaluate_report.json`（门禁 6.1 全量指标）；
3. ONNX 导出（或本地推理服务 `serve_reranker.py`）——供 Go 引擎接入（`docs/training/MODEL_SERVING.md`）；
4. 训练数据快照（`--output-dir` 内可复现）；
5. 若用 LoRA：合并后的完整权重（不是 adapter 单独）。

---

## 2. P1：领域 Embedding 替换/微调

### 2.1 任务

用开源 embedding 替换 `voyage-3-lite`，提升召回层质量。**优先直接替换实测，不达标再微调。**

### 2.2 候选（全部开源，先实测后微调）

| 模型 | 参数 | 中文 | License |
|---|---|---|---|
| `BAAI/bge-m3` | 567M | 中文第一、100+ 语言、稠密+稀疏 | Apache 2.0 |
| `Alibaba-NLP/gte-multilingual-base` | — | 中文强 | 需查证 |
| `shibing624/text2vec-base-chinese` | ~110M | 中文匹配 | Apache 2.0 |
| ⚠️ `moka-ai/m3e-base` | ~110M | 中文 | **非商用，禁止** |

### 2.3 实测协议

`scripts/training/bench_embedding.py`：
- 对 `data/bench/*` + `zh_assoc` 全量画像计算 embedding，跑 recall 候选召回（top-20/48），对比：
  - 现状 `voyage-3-lite`（若有 key，`models.embedding` 配置）；无 key 则跳过此列；
  - `bge-m3`（稠密）；
  - `bge-m3`（稠密+稀疏混合）。
- 指标：HR@3 / NDCG@5 / 召回覆盖（对齐 §6 门禁）。
- **结论分支**：
  - 若 bge-m3 ≥ voyage → 走 ADR 替换（改 config + golden 语义变更评审）；
  - 若 bge-m3 不达标但明显优于 voyage → 对比学习轻量微调（正=黄金对、负=干扰对，InfoNCE）；
  - 否则保持 voyage 并记录。

### 2.4 注意事项

- **embedding 变更会改 golden 对拍（逐位）**：必须走 ADR 评审（spec/05-boundaries 语义变更），不允许 hack 测试。
- 维度：引擎配置 `models.embedding_dimensions` 支持 MRL 截断，bge-m3 原生 1024 维。

---

## 3. P2：Extract 四节结构化器（可选）

- 基座：`Qwen/Qwen2.5-0.5B-Instruct` 或 `Qwen2.5-1.5B-Instruct`（Apache 2.0），LoRA 微调（LLaMA-Factory 或 transformers+peft）。
- 数据：合成画像文本 → 四节。**关键**：用 bench 四节画像拼回"自由文本画像"（模板化重写）作输入，四节本身作监督标签。脚本 `scripts/training/train_extract.py` 内置 `make_free_text` 拼装函数并导出 LLaMA-Factory 数据集格式（`extract_data.jsonl`），无需额外参数。
- 训练目标：把 extract 变成本地确定性调用，消除 extract 提示词注入面（#52/#46）。
- 门禁：见 §6.3。

---

## 4. P3：Introduce（明确延后）

本阶段不做。原因：有 `AttachFallbackIntro` 模板兜底、失败不阻塞核心、小模型生成话术质量差距明显；等真实反馈后再考虑。

---

## 5. 不建议训练

- **HyDE 描述器**：若 P1 embedding 做好则边际价值下降；且 #68 有 max-pool 饱和问题。**不投入。**

---

## 6. 评测门禁（交付验收）

### 6.1 P0 Score 打分器（必须全绿）

在 `holdout/scenarios/HT-*.json`（12 陷阱，禁入训练）+ `bench` 测试集上：

| 指标 | 门禁 |
|---|---|
| 黄金对 HR@3 | ≥ 0.60 |
| NDCG@5 | ≥ 0.40 |
| total_envy | ≤ 2 |
| 12 陷阱断言 | 全绿（holdout assertions 逐一满足） |
| 与 v3 LLM 打分一致性 | Spearman ≥ 0.6（在独立 624 对盲标注或本批随机样本上，用 LLM 离线标注比对；脚本当前提供分数排序对比，标注比对需 PM 另配 LLM 标注步骤） |
| 注入/堆砌对抗 | 陷阱集内注入画像不得获高分（对齐 #45/#48 预期） |

评测脚本：`scripts/training/evaluate_reranker.py`（输出 JSON 报告 + 逐陷阱明细）。

### 6.2 P1 Embedding

| 指标 | 门禁 |
|---|---|
| 召回 HR@3 | ≥ 现状 voyage-3-lite 或 bge-m3 ≥ voyage（实测对比） |
| golden 语义 | 替换走 ADR 评审通过 |

### 6.3 P2 Extract

| 指标 | 门禁 |
|---|---|
| 四节抽取准确率（严格匹配/语义匹配） | ≥ 0.85（val） |
| 中文样本 | ≥ 0.80 |
| 注入样本 | 注入文本不被当分节（#46） |

---

## 7. 数据与合规红线

1. **holdout 陷阱永不入训练**（污染 = 评测失效）。
2. **公开数据 license 逐一核查**：LCQMC/BQ/ATEC/PAWS-X/STS-B 多为研究用途；若模型商用部署，**只保留 Apache 2.0/MIT 系基座**（BGE/Qwen/text2vec 可商用）；`m3e-base` 非商用禁止。
3. **合成数据**（bench/extended/lab 生成）自产，无 license 约束。
4. 训练脚本与数据不写入引擎 `golden/` 对拍路径（训练产物放 `scripts/training/output/`，gitignore）。

---

## 8. 环境要求

- GPU：单卡 ≥16GB 显存（bge-reranker-v2-m3 微调）；LoRA 可降至 8GB。
- Python ≥ 3.10；依赖见 `scripts/training/requirements.txt`。
- 拉取开源基座需访问 HuggingFace / ModelScope（网络）。
- 训练产物不引入 AGPL/GPL-3.0/SSPL 依赖（治理硬规则）。

---

## 9. 训练工作流（PM agent 执行顺序）

1. `cd scripts/training && pip install -r requirements.txt`
2. `python prepare_data.py --out-dir ./output/data`（生成训练/评测集；可加 `--synthesize --synthetic-pairs 5000` 扩展规模）
3. `python evaluate_reranker.py --model BAAI/bge-reranker-v2-m3 --data ./output/data --out ./output/data/baseline.json`（zero-shot 基线）
4. `python train_reranker.py --base-model BAAI/bge-reranker-v2-m3 --data ./output/data --out-dir ./output/model`
5. `python evaluate_reranker.py --model ./output/model --data ./output/data --out ./output/data/trained.json`（训练后对比，与基线对照）
6. 若门禁全绿 → 导出 ONNX / 起 serve → 按 `docs/training/MODEL_SERVING.md` 接入 Go 引擎
7. 输出训练报告（含超参、指标、对比），附 PR
