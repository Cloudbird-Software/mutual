# CLAUDE.md — Agent Swarm 施工指令

> 本文件是 agent swarm 的唯一入口。读它，遵它，不要自行做架构决策。

## 1. 项目本质

Mutual 是一个 **LLM 驱动的双向互惠推荐引擎**。核心范式：**spec 驱动，代码可丢弃**。

- spec（`spec/` 目录 + `src/mutual/schemas.py` + `src/mutual/stages.py` + `config/default.yaml` + `tests/golden/`）是唯一真相。
- 实现代码（`src/mutual/` 各模块）只是 spec 的执行体，可以随时重写。
- **你的工作标准**：实现代码的好坏 = 它是否暴露了 spec 的沉默（没说清楚的边界）与缺陷（错误的假设）。发现沉默时，**改 spec 而非 hack 实现**。

## 2. 施工铁律（不可违反）

1. **先读 spec，再写代码**。每次开始工作前，读 `spec/00-overview.md` 和你要实现的 stage 的 spec。
2. **契约不可改**。`schemas.py` 的 dataclass 字段和 `stages.py` 的 StageSpec 的 name/io_schema 不可修改。如需修改，先在 spec 文档中提出变更理由，经审核后再改。
3. **实现是纯变换**。stage 的 `run` 函数不碰文件系统、数据库、网络。一切 IO 归 adapter（`store.py` + `runners.py`）。
4. **golden test 不可绕过**。实现重写后必须逐位通过 golden test。不允许为了让 test 通过而修改 fixture 期望值。
5. **不硬编码参数**。所有可调参数从 `config/default.yaml` 读取。
6. **不引入新依赖**。除非 spec 明确要求。当前依赖：numpy, pyyaml, pytest。FairRec（cvxpy, torch）在 Phase 2 引入。
7. **一次只做一个 stage**。每个 stage 对应一个 PR，PR 标题格式：`[stage:score] 实现 LLM 双向打分`。

## 3. 目录结构

```
mutual/
├── CLAUDE.md                    # 本文件（agent 入口）
├── spec/                        # 唯一真相（不可随意修改）
│   ├── 00-overview.md           # 项目定位、四件套、架构
│   ├── 01-schemas.md            # 数据契约字段级 spec
│   ├── 02-stages.md             # pipeline 阶段声明
│   ├── 03-oracles.md            # Oracle 定义（HR/NDCG + envy）
│   ├── 04-fixtures.md           # Fixture 目录与规则
│   └── 05-boundaries.md         # 显式边界决定（消除沉默）
├── src/mutual/                 # 实现代码（可丢弃重写）
│   ├── schemas.py               # IO 契约（dataclass）✅ 已完成
│   ├── stages.py                # StageSpec 注册表 ✅ 已完成
│   ├── config.py                # 配置加载器 ✅ 已完成
│   ├── llm.py                   # LLM wrapper（Phase 1）
│   ├── store.py                 # Store protocol + FileStore（Phase 1）
│   ├── embed.py                 # Embedding（Phase 1）
│   ├── extract.py               # Profile 提取（Phase 1）
│   ├── hyde.py                  # HyDE 生成（Phase 1）
│   ├── similarity.py            # 方向性相似度（Phase 1）
│   ├── select.py                # 候选对选择（Phase 1）
│   ├── score.py                 # LLM 双向打分 + pre_matrix（Phase 1）
│   ├── match.py                 # NSW 匹配（Phase 2）
│   ├── evaluate.py              # Oracle 计算 HR/NDCG/envy（Phase 2）
│   ├── introduce.py             # 对接话术（Phase 1）
│   ├── report.py                # 匹配报告（Phase 1）
│   ├── runners.py               # 模式运行器（Phase 1）
│   └── __init__.py
├── config/default.yaml          # 默认配置 ✅ 已完成
├── tests/
│   ├── conftest.py              # 公共 fixture ✅ 已完成
│   ├── golden/                  # 可执行 spec（固定答案）
│   │   ├── test_basic/          # 4 人 cohort ✅ 已完成
│   │   └── test_reciprocal/     # 合成市场 ✅ 已完成
│   ├── test_schemas.py          # 契约测试（Phase 0）
│   ├── test_stages.py           # 阶段注册测试（Phase 0）
│   ├── test_golden.py           # Golden 回归测试（Phase 1）
│   └── test_oracles.py          # Oracle 测试（Phase 2）
├── docs/
│   ├── engineering-plan.md      # 工程方案 ✅ 已完成
│   └── ci-gates.md              # CI 门禁定义
├── pyproject.toml               # 项目配置
├── .github/workflows/ci.yml     # CI pipeline
└── CONTRIBUTING.md              # PR 规则与开发流程
```

## 4. 施工顺序（严格按此执行）

### Phase 0 — Spec 打底（✅ 已完成）
- [x] `schemas.py` — 所有 dataclass
- [x] `stages.py` — StageSpec 注册表（stub run）
- [x] `config.py` + `config/default.yaml`
- [x] `tests/golden/test_basic/` — 4 人 cohort fixture
- [x] `tests/golden/test_reciprocal/` — 合成市场 fixture
- [x] `tests/conftest.py` — 公共 fixture
- [ ] `tests/test_schemas.py` — 契约测试
- [ ] `tests/test_stages.py` — 阶段注册测试
- [ ] `pyproject.toml` — 项目配置
- [ ] `.github/workflows/ci.yml` — CI pipeline

### Phase 1 — 可丢弃骨架跑通
按此顺序实现各 stage（每个一个 PR）：
1. `llm.py` — LLM wrapper（cache, run_coro_blocking）
2. `store.py` — Store protocol + FileStore
3. `extract.py` — Profile 提取
4. `hyde.py` — HyDE 生成
5. `embed.py` — Embedding 生成
6. `similarity.py` — 方向性相似度（compute_similarity）
7. `select.py` — 候选对选择（select_pairs）
8. `score.py` — LLM 双向打分 + pre_matrix（build_pref_matrix）
9. `introduce.py` — 对接话术
10. `report.py` — 匹配报告（create_report）
11. `runners.py` — 模式运行器（串联以上）
12. `tests/test_golden.py` — Golden 回归测试（断言分层，见 spec/05-boundaries.md §11）

### Phase 2 — 互惠求解接入
1. `match.py` — NSW 求解 + envy 检查（集成 FairRec）
2. `evaluate.py` — Oracle 计算（evaluate）
3. `tests/test_oracles.py` — Oracle 测试 + 激活 golden 的 NSW/envy 断言

### Phase 3 — 评测闭环
1. 评测循环（复用 AgentRecBench Simulator 模式）
2. LLM 自我改进反馈注入

## 5. 每个 stage 的实现规范

```python
# src/mutual/<stage>.py

from .schemas import <InputType>, <OutputType>

def create_<output>(<input params>) -> <OutputType>:
    """<stage description from spec/02-stages.md>
    
    边界：
    - <from spec/05-boundaries.md>
    """
    # 纯变换：不碰文件系统、数据库、网络
    ...
    return <OutputType>(...)

def load_<output>(path) -> <OutputType>:
    """从磁盘加载（adapter 用）。"""
    ...

def dump_<output>(data, path) -> None:
    """写入磁盘（adapter 用）。"""
    ...
```

实现完成后，到 `stages.py` 把对应 StageSpec 的 `_stub_run` / `_stub_load` / `_stub_dump` 替换为真实函数。

## 6. PR 规则

- **一个 stage 一个 PR**。PR 标题：`[stage:<name>] <brief>`。
- **PR 必须包含**：
  1. 实现代码
  2. 对应的单元测试
  3. `stages.py` 中 stub → 真实函数的替换
  4. 如有 spec 变更，单独的 spec PR
- **PR 描述模板**：
  ```
  ## Stage: <name>
  ## Spec 引用: spec/02-stages.md §<N>
  ## 边界处理: <列出 spec/05-boundaries.md 中相关的边界>
  ## 测试: <列出新增测试>
  ## Breaking changes: <如有>
  ```
- **CI 必须通过**：lint + type check + golden test + 评测门禁。

## 7. 禁止事项

- ❌ 修改 `schemas.py` 的 dataclass 字段（先改 spec）
- ❌ 修改 `stages.py` 的 StageSpec name/io_schema（先改 spec）
- ❌ 修改 golden fixture 期望值（除非 spec 变更）
- ❌ 在 stage 的 `run` 函数中做 IO（文件/DB/网络）
- ❌ 硬编码参数（一律从 config 读取）
- ❌ 使用 Python 内置 `hash()` 做缓存 key
- ❌ 引入 spec 未要求的新依赖
- ❌ 跳过 CI 门禁
