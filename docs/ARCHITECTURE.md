# Mutual 架构（Go + BAML 实现）

> 本文是 Go+BAML 实现的架构总图。契约语义见 `spec/`；实现导航见 `docs/AI-GUIDE.md`。
> 分层规则由 `cmd/archlint` 机器强制（`make arch`），本文是它的可读版本。

## 1. 分层模型

核心原则：**低层不得依赖高层**。每层只向下看，保证核心算法（纯变换）不被
IO/编排细节污染——这是 spec/02-stages.md "纯变换 / 适配层" 分层在 Go 里的落地。

```
L3 入口层   cmd/mutual          CLI（evaluate / calibrate）
            cmd/archlint        依赖边界检查器（治理工具）

L2 适配层   internal/pipeline   11 阶段编排（唯一知道全流程的地方）
            internal/bench      三场景评测 + 合成市场（离线 oracle）
            internal/store      文件持久化（路径穿越守卫）
            internal/feedback   LLM 自改进闭环（权重/prompt 校准）
            internal/bamlllm    BAML 类型化客户端 ↔ engine.LLMClient 桥接
            config              YAML 配置（参数层，代码不硬编码参数）

L1 核心层   internal/engine     确定性算法核心（11 个阶段函数，纯变换）
            internal/signal     surrogate/fake 信号源（离线评测替身）

L0 基础层   internal/domain     强类型数据契约（对应 spec/01-schemas.md）
            internal/rng        NumPy 兼容 MT19937（golden 逐位对拍依赖）
            internal/num        glibc log 位级移植 + pairwise 求和

叶子        baml_client/        BAML 生成代码（LLM prompt 契约，勿手改）
```

依赖方向检查：`go run ./cmd/archlint`（CI 的 `make arch` 门禁）。
新增 `internal/` 包必须登记进 `cmd/archlint/main.go` 的 `layerOf`，否则视为未分层（fail-closed）。

## 2. 一个请求的完整路径

以 `pipeline.RunFullMatch`（N×N 全量匹配）为例，数据从上到下流过各层：

```
Profile[]（自由文本画像）
  │
  ├─ extract   engine.ExtractSections      LLM → 四分节（skills/vision/project/needs）
  ├─ hyde      engine.GenerateHyde         LLM → 假设性描述（增强语义召回）
  ├─ embed     engine.EmbedProfiles        Embedder → [N][S][D] 张量（content-addressed 复用）
  ├─ similarity engine.ComputeSimilarity   张量 → 方向性相似度（无盲对称化）
  ├─ select    engine.SelectCandidates     相似度 → 候选对（贪心，预算前置）
  ├─ score     engine.ScorePairs           LLM → A→B / B→A 双向分数（批量+预算）
  ├─ pre_matrix engine.BuildPrefMatrix     分数 → PrefMatrix（LLM 缺分 embed 兜底）
  ├─ match     engine.SolveMatching        NSW 全局互惠最优（ envy 公平检查）
  ├─ introduce engine.GenerateIntroductions LLM → 双向话术（失败模板兜底）
  └─ report    engine.BuildReport          聚合输出（含 novelty 排除）
```

关键边界（spec/05-boundaries.md 的实现映射）：

- **LLM 一切失败都有降级路径**：extract → NotSpecified 占位；score → 未打分
  保留 embed 权重；introduce → 模板话术。pipeline 永不因单次 LLM 失败中断。
- **确定性**：RNG 消费顺序、map 遍历序、浮点求和方式全部固定
  （`internal/rng` + `internal/num`），同一输入两次运行逐位一致。
- **纯变换**：engine 包内函数不触网、不落盘、不读全局状态；LLM 与
  Embedder 经接口注入（`engine.LLMClient` / `engine.Embedder`）。

## 3. LLM 集成：两条通路

### 3.1 离线通路（CI / golden test）

`internal/signal` 提供 LLM/embedder 替身：FakeLLM 按 prompt 内容查表返回
固定响应（spec/04-fixtures.md §7.1 契约），Surrogate 从画像文本计算确定性
语义信号。CI 无需 API 凭据，全链路可复现。

### 3.2 在线通路（生产）

`internal/bamlllm.Client` 实现 `engine.LLMClient`，桥接到 BAML 类型化客户端：

```
engine 阶段函数 --字符串 prompt--> bamlllm 路由（按 §7.1 标记约定）
    --> 还原类型化输入 --> baml_client.ScorePairs / ExtractProfile / ...
    --> 类型化结果序列化回 JSON --> engine 各解析器
```

prompt 契约定义在 `baml_src/*.baml`（唯一事实来源），生成物 `baml_client/`
提交入库——离线可构建，CI 不跑 codegen。变更流程：
改 `baml_src` → `npx @boundaryml/baml@0.226.1 generate` → 同步更新
`golden/baml/` 快照（`internal/goldentest` 门禁强制三者同步）。

## 4. Golden 差分对拍体系

Go 实现的正确性 = 与 Python 基线逐位一致。对拍资产在 `golden/`：

| 目录 | 内容 | 守护测试 |
|---|---|---|
| `golden/rng/` | MT19937 流 + glibc log 向量 | `internal/rng`、`internal/num` |
| `golden/domain/` | hash_text 向量 | `internal/domain` |
| `golden/engine/` | 全链路中间产物（full_flow.json） | `internal/engine`、`internal/pipeline` |
| `golden/test_basic/` | cohort 级 fixtures | `internal/bench` |
| `golden/test_reciprocal/` | 合成市场矩阵 | `internal/bench` |
| `golden/evaluation_report.json` | 评测门禁报告 | `internal/bench`、`cmd/mutual` |
| `golden/baml/` | prompt 契约快照 | `internal/goldentest` |

捕获脚本：`scripts/capture_golden_engine.py`（在 Python 基线上运行，
产出 golden JSON——Python 面移除后不可再生成，变更即失效）。

## 5. 目录索引

```
mutual/
├── cmd/mutual/          CLI 入口（evaluate 门禁 / calibrate 反馈闭环）
├── cmd/archlint/        分层依赖检查器
├── config/              default.yaml + YAML 子集解析 + prompt 模板
├── internal/
│   ├── domain/          强类型契约：UserID/PairID/Edge/MatchResult/EvaluationReport...
│   ├── num/             glibc log 位级移植（NumPy randn 依赖）
│   ├── rng/             MT19937（NumPy RandomState 语义）
│   ├── engine/          11 阶段纯变换 + LLMClient/Embedder 接口
│   ├── signal/          FakeLLM/FakeEmbedder/Surrogate（离线替身）
│   ├── bamlllm/         BAML 桥接适配器
│   ├── store/           FileStore（SafeFilename 路径穿越守卫）
│   ├── pipeline/        RunFullMatch / RunQueryMatch / RunBatchMatch
│   ├── bench/           classic/drift/cold 三场景 + market 构造性 oracle
│   ├── feedback/        CalibrateWeights / CalibratePrompts / MatchMemory
│   └── goldentest/      BAML 快照门禁
├── baml_src/            LLM prompt 契约（唯一事实来源，勿手改生成物）
├── baml_client/         BAML 生成代码（提交入库，离线可构建）
├── golden/              Python 基线捕获的差分对拍参考值
├── spec/                契约语义（schema/stage/oracle/fixture/boundary）
└── scripts/             golden 捕获 + 依赖检查（Python 面遗留）
```

## 6. 设计决策记录

架构级决策见组织 `agent-registry/decisions/ADR-0027`（Go+BAML 重写立项）。
本仓库内的实现级权衡（如 glibc log 移植原因、map 排序确定性）记录在
各包的 doc 注释里——`go doc` 可直接检索。
