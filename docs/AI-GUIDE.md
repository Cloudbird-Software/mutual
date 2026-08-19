# AI 协作指南（AI-GUIDE）

> 本文写给在本仓库工作的 AI agent（也适用于新成员）。目标：**5 分钟建立
> 正确的心智模型，改代码不踩契约边界**。架构总图见 `docs/ARCHITECTURE.md`，
> 契约语义见 `spec/`。

## 0. 铁律（违反 = CI 红 = PR 拒绝）

1. **Spec 是唯一真相**。发现行为不合理时，先查 `spec/05-boundaries.md`
   是否已有显式决定；没有 → 改 spec（独立 PR），而非顺手 hack 实现。
2. **Golden 逐位一致不可协商**。`golden/` 是 Python 基线捕获的参考值，
   任何"数值差一点点"都是 bug，不是浮点误差可以搪塞——RNG/求和/取整
   已做位级兼容（`internal/rng`、`internal/num`）。
3. **分层单向依赖**。低层不得 import 高层（`make arch` 强制）。新增
   `internal/` 包必须登记进 `cmd/archlint/main.go` 的 `layerOf`。
4. **prompt 变更走快照流程**。改 `baml_src/*.baml` 必须同步
   `npx @boundaryml/baml@0.226.1 generate` + 更新 `golden/baml/`。
5. **参数不硬编码**。可调参数只活在 `config/default.yaml`；引擎代码里
   出现魔法数字 = 违规。

## 1. 我要改 X，去哪个文件

| 想做的事 | 唯一入口 | 配套测试 |
|---|---|---|
| 算法行为（召回/打分/匹配） | `internal/engine/<stage>.go` | `engine_golden_test.go` |
| LLM prompt 措辞 | `baml_src/*.baml`（不是生成物！） | `internal/goldentest` |
| 默认模板兜底 | `config/templates.go` | `config/config_test.go` |
| 可调参数 | `config/default.yaml` | `config/config_test.go` |
| 管线编排/新模式 | `internal/pipeline/pipeline.go` | `pipeline_golden_test.go` |
| 评测场景 | `internal/bench/scenario.go` | `bench_golden_test.go` |
| 门禁阈值 | `config/default.yaml` `evaluation.gates` | `cmd/mutual/main_test.go` |
| 存储/持久化 | `internal/store/filestore.go` | `store/store_test.go` |
| 离线 LLM 替身行为 | `internal/signal/` | `signal/signal_test.go` |
| CLI 行为 | `cmd/mutual/main.go` | `main_test.go` |

## 2. 阶段函数的统一形状

engine 的每个阶段都是同构的纯变换，读任一文件即可类推全部：

```go
// 输入：上一阶段产物 + 注入的 LLM/Embedder + config 视图
// 输出：本阶段产物 + 失败报告（failedIDs/unscored——绝不静默丢弃）
func StageXxx(in StageXxxInput, ...) (out OutType, failures FailureType)
```

- **失败永远显式返回**，调用方决定降级路径（spec/05-boundaries.md）。
- **map 遍历必须排序**（`sortedSectionNames` 模式）——Go map 无序，
  遍历序泄漏 = golden 对拍失败。
- **返回的 Order 切片承载插入序**（对应 Python dict 的有序性），
  golden 对拍依赖它。

## 3. 测试金字塔（全离线，无 API 凭据）

```
单测        config / store / signal / feedback / bamlllm / cmd
golden 对拍  engine / pipeline / bench / domain / rng / num
契约快照    internal/goldentest（BAML prompt 三方同步）
门禁        go run ./cmd/mutual evaluate --fail-on-gate
            （HR@3≥0.6 / NDCG@5≥0.4 / total_envy≤2）
```

跑法：

```bash
make go-test          # 全部测试
make check         # vet + archlint + test + evaluate 门禁
```

golden 对拍失败时的排查顺序：
1. `git diff golden/` —— golden 被人动过？（golden 变更需要 PR 说明）
2. 是否引入了新的 map 遍历 / 未排序输出？
3. 是否改动了 RNG 消费顺序（每次 rand 调用都有位置语义）？
4. 都不是 → 实现与 Python 基线语义分歧，回 `spec/` 找答案。

## 4. 类型系统速查

强类型 ID（`internal/domain/ids.go`）贯穿全链路，禁止裸字符串传参：

```go
domain.UserID     // 用户 id
domain.PairID     // "user1__user2" 复合 id（排序对齐语义）
domain.SectionName // skills/vision/project/needs
domain.Edge       // 匹配边（含 intro/starter_topics）
domain.MatchResult // 图 + 边 + envy 报告
domain.EvaluationReport // HR/NDCG/envy（golden 顶层形状）
```

Python 兼容函数（`internal/domain/pycompat.go`）：`PyRound`（banker's
rounding）、`HashText`（md5 hex）、`PyReprFloat`——所有与 golden JSON
比较的数值都必须经它们转换。

## 5. 常见任务流

### 5.1 修改算法

1. 读 `spec/02-stages.md` 对应阶段 + `spec/05-boundaries.md` 相关边界。
2. 改 `internal/engine/<stage>.go`。
3. `make go-test` —— 若 golden 对拍失败且你的语义变更是 spec 背书的，
   需要**同时**更新 golden（并在 PR 里给出 Python 侧重捕获证据）。

### 5.2 修改 prompt

1. 改 `baml_src/*.baml`。
2. `npx @boundaryml/baml@0.226.1 generate`（版本必须与 generators.baml 一致）。
3. `cp baml_src/*.baml golden/baml/`。
4. PR 三件套齐：baml_src + baml_client + golden/baml（缺一被快照门禁拦截）。

### 5.3 新增评测场景

1. `internal/bench/scenario.go` 加场景构造。
2. 用 Python 基线（`scripts/capture_golden_engine.py` 同法）捕获期望值。
3. `bench_golden_test.go` 加对拍断言。

### 5.4 新增 internal 包

1. 确定层级（见 `docs/ARCHITECTURE.md` §1）。
2. 登记 `cmd/archlint/main.go` 的 `layerOf`（不登记 = 漏检，fail-closed）。
3. 包注释写清定位与对应 spec 章节——这是 AI 阅读的锚点。

## 6. 不要做

- 不要手改 `baml_client/`（生成物，下次 generate 会覆盖）。
- 不要在 engine 包里 import store/bench/pipeline（archlint 拦截）。
- 不要绕过 `domain` 类型直接传 string（弱化类型 = 提高 AI 误读率）。
- 不要捕获 golden 之外的"新参考值"（多真相源 = 对拍体系崩溃）。
- 不要在 push 前 skip 测试——`make check` 是 PR 的最低门槛。
