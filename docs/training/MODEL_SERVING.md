# mutual 小模型接入引擎说明（MODEL_SERVING）

> 训练产物（P0 Score 打分器 / P1 Embedding / P2 Extract）如何接入 Go 引擎。
> 接入原则：**引擎 `LLMClient` 是四方法接口，实现方负责传输细节；训练好的小模型封装为一个新 client，pipeline 零改动。**

---

## 0. 接入全景

```
┌────────────────────────── 训练侧（Python，scripts/training/）──────────────────────────┐
│  train_reranker.py → 模型权重 → serve_reranker.py（本地推理服务，HTTP /predict）        │
│  或 ONNX 导出（onnxruntime 直接推理）                                                  │
└────────────────────────────────────┬──────────────────────────────────────────────────┘
                                     │ HTTP（JSON）或 ONNX 二进制
┌────────────────────────────────────▼──────────────────────────────────────────────────┐
│  引擎侧（Go，internal/engine/）                                                        │
│  RerankerScoreClient（新实现，满足 engine.LLMClient）                                  │
│  → CompleteScore(prompt, model) 解析 pair → 调小模型 → 返回 DirectionalPairScore JSON  │
│  → pipeline 通过依赖注入使用（config models.pair_llm 指向 "reranker-local"）            │
└───────────────────────────────────────────────────────────────────────────────────────┘
```

---

## 1. P0 Score 打分器接入

### 1.1 服务端（Python，已提供 `scripts/training/serve_reranker.py`）

- 启动：`python serve_reranker.py --model ./output/model --port 8000`
- 接口：`POST /predict`，body：
  ```json
  {"a_sections": {"skills": "...", "vision": "...", "project": "...", "needs": "..."},
   "b_sections": {"skills": "...", "vision": "...", "project": "...", "needs": "..."}}
  ```
  响应：
  ```json
  {"a_to_b": 0.82, "b_to_a": 0.65}
  ```
- 批量：`POST /predict_batch`，body 为 pair 数组，一次最多 `n_profiles_to_score_together` 对。
- 可选安全：绑定 `127.0.0.1` + 简单 token header（防止引擎侧无鉴权暴露）。

### 1.2 引擎侧（Go，新增 `internal/engine/rerankerscore.go`）

```go
// RerankerScoreClient 用本地小模型打分器实现 engine.LLMClient。
// 只实现 CompleteScore；extract/hyde/introduce 仍走 LLM 或模板。
type RerankerScoreClient struct {
    endpoint string // 如 http://127.0.0.1:8000
    http     *http.Client
    model    string
}
func (c *RerankerScoreClient) CompleteScore(prompt, model string) (string, error) {
    // 1) 解析 prompt 中的 "### Pair N: (u1, u2)" 块 + sections
    // 2) 调 /predict_batch
    // 3) 组 DirectionalPairScore JSON（与 bamlllm 输出同构）
}
```

- **关键**：返回的 JSON 必须与 `bamlllm` 解析器期望的 `DirectionalPairScore` 结构逐字一致（`a_to_b`/`b_to_a`/`reasoning`），否则 `parseScoreResponse` 解析失败。reasoning 可由模型生成或填空字符串（引擎不强制内容）。
- 注入方式：`config models.pair_llm` 设 `"reranker-local"`，`pipeline.Deps.LLM` 在启动时按此选择实现（在 `cmd/mutual/main.go` 装配）。

### 1.3 ONNX 替代（无 HTTP 时）

- `scripts/training/export_onnx.py`（基于 optimum / transformers ONNX 导出）。
- Go 侧用 `github.com/yalue/onnxruntime_go` 加载，直接推理——减少一个服务进程，但增加构建依赖与部署复杂度；**默认推荐 HTTP 服务**（隔离、易灰度、易替换）。

---

## 2. P1 Embedding 接入

- 仅替换配置即可（无需改代码）：
  ```yaml
  models:
    embedding: "BAAI/bge-m3"     # 替换 voyage-3-lite
    embedding_dimensions: 1024   # bge-m3 维度
  ```
- 引擎的 embedding 调用在 `internal/engine/embed.go`（`Embedder` 接口）。若直接换 API 不可行（引擎当前走 OpenAI 兼容 embedding API），需在 `internal/embedding/` 新增一个本地推理 client（参考 `RerankerScoreClient` 模式，HTTP 调用 `serve_embedding.py` 或 ONNX）。
- ⚠️ **ADR 评审**：embedding 变化改变所有向量的数值分布 → golden 对拍逐位变化。必须：
  1. 更新 `golden/evaluation_report.json` 与相关对拍期望（走 ADR 流程，不允许 hack 测试）；
  2. 在 CHANGELOG.md 记录对外行为变更。

---

## 3. P2 Extract 接入

- 新 client 实现 `CompleteExtract`（本地 Qwen2.5 服务，LLaMA-Factory 起 vLLM/API 或 `serve_extract.py`）。
- 返回必须与 `baml_src/extract.baml` 输出契约一致（四节 JSON），否则 `parseExtractResponse` 失败。
- 注入：`models` 配置区分 `extract_llm` 与 `pair_llm`（当前二者共用 `pair_llm`；如需分离在 config 增字段）。

---

## 4. 灰度与回滚

1. **灰度**：先小规模（一个场景）跑本地 client，对比 `golden/evaluation_report.json` 与 LLM 基线的 HR/NDCG/envy；
2. **回滚**：config 改回 `pair_llm: "LongCat-2.0"` 即回滚（引擎无状态依赖）；
3. **监控**：本地服务失败时，`CompleteScore` 返回 error → 引擎现有降级路径（未打分保留 embed 权重）自动兜底——**不会崩**。

---

## 5. 验收清单（接入 PR）

- [ ] 本地服务端到端：`serve_reranker.py` 起服务 → Go 端 `CompleteScore` 返回合法 JSON；
- [ ] `make check` 全绿（新增 client 不影响 golden 默认路径——**默认仍是 LLM**）；
- [ ] 小规模灰度 HR@3/NDCG@5 ≥ 门禁；
- [ ] 回滚验证：改配置回 LLM 后行为与基线一致；
- [ ] CHANGELOG 记录接入点。
