# mutual 训练工具（scripts/training/）

> 给训练项目经理（PM agent）与训练工程师的工具箱：把 mutual 的高价值小模型
> 训练需求、数据准备、训练、评测、接入引擎串成一条可执行链路。
> 规格详见 `docs/training/TRAINING_SPEC.md`（验收门禁）、`OPENSOURCE_REFERENCES.md`（开源借鉴）、`MODEL_SERVING.md`（接入引擎）。

## 概览

| 脚本 | 用途 | 状态 |
|---|---|---|
| `prepare_data.py` | 合成数据（bench/extended/holdout）→ 训练/评测格式 | ✅ |
| `train_reranker.py` | P0 Score 双向打分器微调（CrossEncoder） | ✅ |
| `evaluate_reranker.py` | 评测门禁（HR@K/NDCG/AUC + holdout 陷阱） | ✅ |
| `serve_reranker.py` | 本地推理服务（供 Go 引擎 HTTP 调用） | ✅ |
| `bench_embedding.py` | P1 Embedding 替换实测（bge-m3 等对比） | ✅ |
| `train_extract.py` | P2 Extract 四节微调（LLaMA-Factory 导出） | ✅（可选） |

## 快速开始（P0 主链路）

```bash
cd scripts/training
pip install -r requirements.txt

# 1) 生成训练/评测数据（bench + extended + zh_assoc；--synthesize 可扩展至万对）
python prepare_data.py --out-dir ./output/data --synthesize --synthetic-pairs 5000

# 2) zero-shot 基线（训练前必跑，记录对照）
python evaluate_reranker.py --model BAAI/bge-reranker-v2-m3 --data ./output/data \
    --out ./output/data/baseline.json

# 3) 微调
python train_reranker.py --base-model BAAI/bge-reranker-v2-m3 \
    --data ./output/data --out-dir ./output/model

# 4) 训练后评测（对比基线）
python evaluate_reranker.py --model ./output/model --data ./output/data \
    --out ./output/data/trained.json
```

## P1 Embedding 替换实测

```bash
python bench_embedding.py --data ./output/data \
    --models BAAI/bge-m3 shibing624/text2vec-base-chinese
```
替换后走 ADR 评审（见 TRAINING_SPEC §2.4）。

## P2 Extract 微调（可选）

```bash
python train_extract.py --data ./output/data --out-dir ./output/extract-model
# 按脚本输出的 LLaMA-Factory CLI 在显卡环境执行
```

## 接入引擎

训练完成后：
1. 起服务：`python serve_reranker.py --model ./output/model --port 8000`
2. 引擎新增 `RerankerScoreClient`（实现 `engine.LLMClient.CompleteScore`），`config models.pair_llm` 指向 `reranker-local`。
   详见 `docs/training/MODEL_SERVING.md`。

## 门禁（交付验收，TRAINING_SPEC §6）

| 产物 | 门禁 |
|---|---|
| P0 Score | HR@3≥0.6 / NDCG@5≥0.4 / envy≤2 / 12 陷阱全绿 / 与 LLM 一致性 Spearman≥0.6 |
| P1 Embedding | 召回 HR@3 ≥ 现状 voyage 或 bge-m3 ≥ voyage |
| P2 Extract | 四节抽取准确率 ≥0.85（中文 ≥0.80）、注入样本不误提取 |

## 目录约定

- 训练产物（`output/`）由 `.gitignore` 忽略，不入库（避免大文件污染仓库）。
- 数据：`data/bench` + `data/bench-extended` 是训练源；`holdout/scenarios` 只做评测，**永不入训练**。
- 许可证：所有推荐基座与框架均为 Apache 2.0 / MIT 可商用（`m3e-base` 非商用已排除）。
