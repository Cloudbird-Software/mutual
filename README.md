# Mutual

> **LLM 驱动的双向互惠推荐引擎。** Spec 驱动，代码可丢弃。Go + BAML 实现。

Mutual 用于协会会员互识、商业机会推荐、招商与投资对接。与传统"排序式"推荐不同，它做的是**匹配**：A 推荐 B，必须同时满足 B 也愿意对接 A。

---

## 1. 项目简介

Mutual 是一个 **LLM 驱动的双向互惠推荐引擎**。核心范式：**spec 驱动，代码可丢弃**。

- spec（`spec/` 目录 + `config/default.yaml` + `golden/`）是唯一真相。
- 实现代码（Go `internal/` 各包）只是 spec 的执行体，可以随时重写。
- 衡量实现好坏的标准是：它是否暴露了 spec 的沉默（没说清楚的边界）与缺陷（错误的假设）。发现沉默时，**改 spec 而非 hack 实现**。

当前实现为 **Go + BAML**（ADR-0027）：`internal/engine` 承载 11 阶段确定性
核心，`baml_src/` 用类型化契约定义全部 LLM prompt。Go 实现与 Python 基线
做 **golden 逐位对拍**——同一输入，浮点结果逐位一致，证明重写零语义漂移。

### 核心特征

| 特征 | 含义 |
|---|---|
| **双向互惠** | A 推荐 B 必须同时满足 B 也愿意对接 A，是"匹配"而非"排序"。LLM 对每对做 A→B、B→A 双向打分，方向性不盲目对称化。 |
| **自然语言丛林** | 实体只有自由文本画像（简历、需求、愿景、项目），无确定属性标签。LLM 是唯一能做语义判断的组件。 |
| **spec 驱动** | strongDM 范式——契约是唯一真相，实现代码可丢弃重写。schema/stage/config/golden 四件套即 spec。 |
| **开发期零真实数据** | 全部用 benchmark 与合成 fixture，不接触真实协会数据。golden fixture 提供"固定输入→固定输出"的可执行 spec。 |
| **AI 可读性优先** | 强类型 ID 贯穿全链路、包注释即导航、分层依赖机器强制（`cmd/archlint`）、文档系统面向 agent 优化。 |

### 三层漏斗 + 互惠求解

```
召回（embedding 语义初筛，全量低成本）
  → 精排（LLM 双向打分 A→B / B→A，预算上限）
    → 匹配（NSW 全局一致 + envy 公平性，确定性可复现）
```

- 召回层：用 embedding 把全量 N×N 降到可承受的候选对数。
- 精排层：LLM 对候选对做双向语义打分，方向性不盲目对称化。
- 匹配层：把 LLM 分数落入 `PrefMatrix`，交给 NSW 求解器做全局互惠最优。

---

## 2. 快速开始

```bash
# Go 面（主实现）：测试 + 依赖边界 + 评测门禁，全离线，无需 LLM 凭据
make check

# 只跑评测门禁（HR@3≥0.6 / NDCG@5≥0.4 / total_envy≤2）
go run ./cmd/mutual evaluate --fail-on-gate

# 校准闭环（按评测历史调整融合权重 / prompt）
go run ./cmd/mutual calibrate --history reports.json
```

LLM 接入（生产）：`internal/bamlllm.Client` 实现 `engine.LLMClient`，
prompt 契约在 `baml_src/*.baml`。变更流程见
[docs/AI-GUIDE.md §5.2](docs/AI-GUIDE.md)。

> Python 基线（`src/`）在双栈过渡期保留，仅用于 golden 参考值捕获
> （`scripts/capture_golden_engine.py`），最终将被移除。

---

## 3. 目录结构

```
mutual/
├── spec/                        # 唯一真相（不可随意修改）
│   ├── 00-overview.md           # 项目定位、四件套、架构
│   ├── 01-schemas.md            # 数据契约字段级 spec
│   ├── 02-stages.md             # pipeline 阶段声明
│   ├── 03-oracles.md            # Oracle 定义（HR/NDCG + envy）
│   ├── 04-fixtures.md           # Fixture 目录与规则
│   └── 05-boundaries.md         # 显式边界决定（消除沉默）
├── internal/                    # Go 实现（可丢弃重写）
│   ├── domain/                  # 强类型契约：UserID/PairID/Edge/MatchResult...
│   ├── num/                     # glibc log 位级移植（NumPy randn 兼容）
│   ├── rng/                     # MT19937（NumPy RandomState 语义）
│   ├── engine/                  # 11 阶段纯变换 + LLMClient/Embedder 接口
│   ├── signal/                  # Fake/Surrogate 信号源（离线评测替身）
│   ├── bamlllm/                 # BAML 类型化客户端桥接
│   ├── store/                   # FileStore（路径穿越守卫）
│   ├── pipeline/                # RunFullMatch / RunQueryMatch / RunBatchMatch
│   ├── bench/                   # 三场景评测 + 合成市场 oracle
│   ├── feedback/                # LLM 自改进闭环（权重/prompt 校准）
│   └── goldentest/              # BAML prompt 快照门禁
├── cmd/
│   ├── mutual/                  # CLI：evaluate / calibrate
│   └── archlint/                # 分层依赖边界检查器
├── config/default.yaml          # 默认配置（可调参数集中地）
├── baml_src/                    # LLM prompt 契约（唯一事实来源）
├── baml_client/                 # BAML 生成代码（提交入库，勿手改）
├── golden/                      # Python 基线捕获的差分对拍参考值
├── docs/
│   ├── ARCHITECTURE.md          # Go+BAML 架构总图（分层/数据流/桥接）
│   ├── AI-GUIDE.md              # AI 协作指南（改哪/怎么改/铁律）
│   ├── engineering-plan.md      # 工程方案（施工蓝图）
│   ├── ci-gates.md              # CI 门禁定义
│   └── training/                # 小模型训练：规格/开源借鉴/接入引擎
├── scripts/training/            # 训练工具（合成数据→微调→评测→推理服务）
└── .github/workflows/ci.yml     # CI pipeline（双栈：check + go）
```

---

## 4. 四件套（唯一真相）

Mutual 的 spec 由四件套组成，它们共同构成项目的唯一真相源。实现代码可以随时重写，但四件套不可随意修改。

| 件 | 文件 | 作用 |
|---|---|---|
| **schema** | `internal/domain/` + `spec/01-schemas.md` | IO 契约：每个数据结构的字段、类型、语义。强类型 + JSON 往返一致性由测试守护。 |
| **stage** | `internal/engine/` + `spec/02-stages.md` | 变换声明：每阶段输入/输出/纯函数。包注释即阶段文档（`go doc` 可检索）。 |
| **config** | `config/default.yaml` | 可调参数：blending、budget、degree、prompt。实现代码不硬编码参数，一律从 config 读取。 |
| **golden** | `golden/` + `spec/04-fixtures.md` | 可执行 spec：固定输入→固定输出。实现重写后必须逐位通过 golden 对拍，不允许为了让 test 通过而修改期望值。 |

### 关键约束

- **实现是纯变换**：`internal/engine` 不碰文件系统、数据库、网络。一切 IO 归适配层（`store` / `pipeline` / `bamlllm`）。
- **分层单向依赖**：低层不得 import 高层，`cmd/archlint` 机器强制（`make go-arch`）。
- **不硬编码参数**：所有可调参数从 `config/default.yaml` 读取。
- **确定性**：同输入两次运行逐位一致（RNG 流/求和/遍历序全部固定）。

> 完整施工铁律见 [CLAUDE.md](CLAUDE.md)，边界决定见 [spec/05-boundaries.md](spec/05-boundaries.md)。

---

## 5. 评测闭环

双指标离线可算，无需真实用户：

| 指标 | 来源 | 衡量维度 |
|---|---|---|
| HR@1/3/5、NDCG@5 | AgentRecBench | 推荐质量：该推荐的有没有被推荐 |
| envy-freeness | FairRec | 互惠公平：双方是否都受益 |

评测通过标准写入 spec，CI 门禁强制执行：

- `HR@3 >= 0.6`
- `NDCG@5 >= 0.4`
- `total_envy <= 2`

门禁数值与来源见 [spec/03-oracles.md](spec/03-oracles.md)、[docs/ci-gates.md](docs/ci-gates.md)。

---

## 6. 开发流程

1. 先读 spec（`spec/00-overview.md` + 目标 stage 的 spec）。
2. 按 [docs/AI-GUIDE.md §1](docs/AI-GUIDE.md) 定位唯一入口文件。
3. 实现 / 修改（保持纯变换与类型纪律）。
4. `make check` 全绿后提交 PR。
5. golden 对拍失败时先排查确定性（map 遍历序 / RNG 消费顺序），再怀疑语义分歧。

完整流程、PR 规则与 review checklist 见 [CONTRIBUTING.md](CONTRIBUTING.md)。

---

## 许可证

MIT
