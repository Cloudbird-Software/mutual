# Mutual

> **LLM 驱动的双向互惠推荐引擎。** Spec 驱动，代码可丢弃。

Mutual 用于协会会员互识、商业机会推荐、招商与投资对接。与传统"排序式"推荐不同，它做的是**匹配**：A 推荐 B，必须同时满足 B 也愿意对接 A。

---

## 1. 项目简介

Mutual 是一个 **LLM 驱动的双向互惠推荐引擎**。核心范式：**spec 驱动，代码可丢弃**。

- spec（`spec/` 目录 + `src/mutual/schemas.py` + `src/mutual/stages.py` + `config/default.yaml` + `tests/golden/`）是唯一真相。
- 实现代码（`src/mutual/` 各模块）只是 spec 的执行体，可以随时重写。
- 衡量实现好坏的标准是：它是否暴露了 spec 的沉默（没说清楚的边界）与缺陷（错误的假设）。发现沉默时，**改 spec 而非 hack 实现**。

### 核心特征

| 特征 | 含义 |
|---|---|
| **双向互惠** | A 推荐 B 必须同时满足 B 也愿意对接 A，是"匹配"而非"排序"。LLM 对每对做 A→B、B→A 双向打分，方向性不盲目对称化。 |
| **自然语言丛林** | 实体只有自由文本画像（简历、需求、愿景、项目），无确定属性标签。LLM 是唯一能做语义判断的组件。 |
| **spec 驱动** | strongDM 范式——契约是唯一真相，实现代码可丢弃重写。schema/stage/config/golden 四件套即 spec。 |
| **开发期零真实数据** | 全部用 benchmark 与合成 fixture，不接触真实协会数据。golden fixture 提供"固定输入→固定输出"的可执行 spec。 |

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
# 克隆仓库
git clone <repo-url> mutual
cd mutual

# 安装开发依赖（含 pytest / ruff / mypy）
pip install -e ".[dev]"

# 运行测试（默认跳过需要真实 LLM 的测试）
pytest tests/
```

需要 FairRec 互惠求解（Phase 2）时安装可选依赖：

```bash
pip install -e ".[fair]"   # 安装 cvxpy, torch
```

### 运行需要真实 LLM 的测试

默认情况下，标记为 `llm` 的测试会被跳过（CI 与本地都不跑）。手动触发：

```bash
RUN_LLM_TESTS=1 pytest tests/ -m "llm"
```

详见 [docs/ci-gates.md](docs/ci-gates.md)。

---

## 3. 目录结构

```
mutual/
├── CLAUDE.md                    # agent swarm 入口指令（施工铁律）
├── spec/                        # 唯一真相（不可随意修改）
│   ├── 00-overview.md           # 项目定位、四件套、架构
│   ├── 01-schemas.md            # 数据契约字段级 spec
│   ├── 02-stages.md             # pipeline 阶段声明
│   ├── 03-oracles.md            # Oracle 定义（HR/NDCG + envy）
│   ├── 04-fixtures.md           # Fixture 目录与规则
│   └── 05-boundaries.md         # 显式边界决定（消除沉默）
├── src/mutual/                 # 实现代码（可丢弃重写）
│   ├── schemas.py               # IO 契约（dataclass）
│   ├── stages.py                # StageSpec 注册表
│   ├── config.py                # 配置加载器
│   ├── llm.py                   # LLM wrapper（Phase 1）
│   ├── store.py                 # Store protocol + FileStore（Phase 1）
│   ├── embed.py                 # Embedding（Phase 1）
│   ├── extract.py               # Profile 提取（Phase 1）
│   ├── hyde.py                  # HyDE 生成（Phase 1）
│   ├── similarity.py            # 方向性相似度（Phase 1）
│   ├── select.py                # 候选对选择（Phase 1）
│   ├── score.py                 # LLM 双向打分 + pre_matrix（Phase 1）
│   ├── match.py                 # NSW 匹配（Phase 2）
│   ├── evaluate.py              # Oracle 计算（Phase 2）
│   ├── introduce.py             # 对接话术（Phase 1）
│   ├── report.py                # 匹配报告（Phase 1）
│   ├── runners.py               # 模式运行器（Phase 1）
│   └── __init__.py
├── config/default.yaml          # 默认配置（可调参数集中地）
├── tests/
│   ├── conftest.py              # 公共 fixture
│   ├── golden/                  # 可执行 spec（固定答案）
│   │   ├── test_basic/          # 4 人 cohort
│   │   └── test_reciprocal/     # 合成市场
│   ├── test_schemas.py          # 契约测试
│   ├── test_stages.py           # 阶段注册测试
│   ├── test_golden.py           # Golden 回归测试（Phase 1）
│   └── test_oracles.py          # Oracle 测试（Phase 2）
├── docs/
│   ├── engineering-plan.md      # 工程方案（施工蓝图）
│   └── ci-gates.md              # CI 门禁定义
├── pyproject.toml               # 项目配置（依赖、lint、type、test）
├── .github/workflows/ci.yml     # CI pipeline
└── CONTRIBUTING.md              # PR 规则与开发流程
```

---

## 4. 四件套（唯一真相）

Mutual 的 spec 由四件套组成，它们共同构成项目的唯一真相源。实现代码可以随时重写，但四件套不可随意修改。

| 件 | 文件 | 作用 |
|---|---|---|
| **schema** | `src/mutual/schemas.py` + `spec/01-schemas.md` | IO 契约：每个数据结构的字段、类型、语义。dataclass 的 `to_dict`/`from_dict` 往返一致性由 `tests/test_schemas.py` 守护。 |
| **stage** | `src/mutual/stages.py` + `spec/02-stages.md` | 变换声明：每阶段输入/输出/纯函数/run·load·dump。`StageSpec` 注册表让外部 caller 无需读源码即可了解每阶段的 IO 契约。 |
| **config** | `config/default.yaml` | 可调参数：blending、budget、degree、prompt。实现代码不硬编码参数，一律从 config 读取。 |
| **golden** | `tests/golden/` + `spec/04-fixtures.md` | 可执行 spec：固定输入→固定输出。实现重写后必须逐位通过 golden test，不允许为了让 test 通过而修改 fixture 期望值。 |

### 关键约束

- **契约不可改**：`schemas.py` 的 dataclass 字段和 `stages.py` 的 StageSpec 的 name/io_schema 不可修改。如需修改，先在 spec 文档中提出变更理由，经审核后再改。
- **实现是纯变换**：stage 的 `run` 函数不碰文件系统、数据库、网络。一切 IO 归 adapter（`store.py` + `runners.py`）。
- **不硬编码参数**：所有可调参数从 `config/default.yaml` 读取。

> 完整施工铁律见 [CLAUDE.md §2](CLAUDE.md)，边界决定见 [spec/05-boundaries.md](spec/05-boundaries.md)。

---

## 5. 评测闭环

双指标离线可算，无需真实用户：

| 指标 | 来源 | 衡量维度 |
|---|---|---|
| HR@1/3/5、NDCG@5 | AgentRecBench | 推荐质量：该推荐的有没有被推荐 |
| envy-freeness | FairRec | 互惠公平：双方是否都受益 |

评测通过标准写入 spec，CI 门禁强制执行（Phase 2+ 启用）：

- `HR@3 >= 0.6`
- `NDCG@5 >= 0.4`
- `total_envy <= 2`

门禁数值与来源见 [spec/03-oracles.md](spec/03-oracles.md)、[docs/ci-gates.md](docs/ci-gates.md)。

---

## 6. 开发流程

1. 先读 spec（`spec/00-overview.md` + 目标 stage 的 spec）
2. 实现该 stage 的 `run`/`load`/`dump`（纯变换，不做 IO）
3. 写对应的单元测试
4. 到 `stages.py` 把 `_stub_run`/`_stub_load`/`_stub_dump` 替换为真实函数
5. 提交 PR（一个 stage 一个 PR，标题格式 `[stage:xxx] 描述`）

完整流程、PR 模板与 review checklist 见 [CONTRIBUTING.md](CONTRIBUTING.md)。

---

## 许可证

MIT
