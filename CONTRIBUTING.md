# 贡献指南（CONTRIBUTING）

> 感谢参与 Mutual。本文件规定 PR 规则与开发流程，所有贡献者必须遵守。
> 施工铁律见 [CLAUDE.md](CLAUDE.md)，CI 门禁定义见 [docs/ci-gates.md](docs/ci-gates.md)。

---

## 1. 开发流程

Mutual 是 spec 驱动项目，**先读 spec，再写代码**。每个 stage 的实现遵循以下五步：

```
读 spec → 实现 stage → 写测试 → 替换 stub → 提 PR
```

### 1.1 读 spec

每次开始工作前，至少读完：

- `spec/00-overview.md` — 项目定位、四件套、架构
- 你要实现的 stage 对应的 `spec/02-stages.md §<N>`
- `spec/05-boundaries.md` — 该 stage 相关的边界决定（消除 spec 沉默）

### 1.2 实现 stage

每个 stage 是一个**纯变换**，遵循 [CLAUDE.md §5](CLAUDE.md) 的实现规范：

```python
# src/mutual/<stage>.py
from .schemas import <InputType>, <OutputType>

def create_<output>(<input params>) -> <OutputType:
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

**铁律**（违反即拒绝合并，详见 [CLAUDE.md §7](CLAUDE.md)）：

- stage 的 `run` 函数不碰文件系统、数据库、网络。一切 IO 归 adapter（`store.py` + `runners.py`）。
- 不硬编码参数，一律从 `config/default.yaml` 读取。
- 不引入 spec 未要求的新依赖（当前依赖见 [pyproject.toml](pyproject.toml)；Phase 2 引入 `cvxpy`/`torch` 走 `[fair]` extras）。
- 不使用 Python 内置 `hash()` 做缓存 key。

### 1.3 写测试

为新增实现写对应的单元测试，放在 `tests/` 下。测试是 spec 的可执行断言。

- 契约测试（`tests/test_schemas.py`）守护 dataclass 往返一致性，字段变化时必须更新。
- golden test（`tests/test_golden.py`）守护固定输入→固定输出，**不允许为了让 test 通过而修改 fixture 期望值**。
- 需要真实 LLM 的测试用 `@pytest.mark.llm` 标记，默认跳过。

### 1.4 替换 stub

实现完成后，到 `src/mutual/stages.py` 把对应 `StageSpec` 的 `_stub_run` / `_stub_load` / `_stub_dump` 替换为真实函数。

- **契约不可改**：`StageSpec` 的 `name` / `io_schema` 不可修改。如需修改，先走 spec 变更流程（见 §6）。

### 1.5 提 PR

本地验证通过后提交 PR：

```bash
# 本地跑 CI 等价的三道门禁
ruff check src tests
ruff format --check src tests
mypy src/
pytest tests/ -m "not llm" --tb=short
```

---

## 2. PR 规则

### 2.1 一个 stage 一个 PR

- 每个 stage 对应一个 PR，**禁止在同一个 PR 中实现多个 stage**。
- PR 标题格式：`[stage:<name>] <简要描述>`，例如 `[stage:score] 实现 LLM 双向打分`。
- 实现顺序遵循 [docs/engineering-plan.md](docs/engineering-plan.md) Phase 1 §3 的严格顺序（`llm.py → store.py → extract.py → ...`）。

### 2.2 PR 必须包含

1. 实现代码（`src/mutual/<stage>.py`）
2. 对应的单元测试
3. `stages.py` 中 stub → 真实函数的替换
4. 如有 spec 变更，**单独的 spec PR**（不与实现混在一起）

### 2.3 PR 描述模板

提交 PR 时，按以下模板填写描述：

```markdown
## Stage: <name>

## Spec 引用
spec/02-stages.md §<N>

## 边界处理
<列出 spec/05-boundaries.md 中与本 stage 相关的边界，以及实现如何处理>

## 测试
<列出新增测试及覆盖的场景>

## Breaking changes
<如有契约或行为变更，明确写出；无则写"无">
```

---

## 3. Review checklist

Review 时逐项检查（Reviewer 与作者自查都用这份）：

- [ ] **spec 一致**：实现与 `spec/02-stages.md` 描述的输入/输出/语义一致。
- [ ] **边界处理**：`spec/05-boundaries.md` 中相关边界都已正确处理，没有靠 hack 绕过。
- [ ] **纯变换**：stage 的 `run` 函数不做 IO（文件/DB/网络），IO 全在 adapter。
- [ ] **无硬编码参数**：所有可调参数从 `config/default.yaml` 读取。
- [ ] **无新依赖**：未引入 spec 未要求的依赖。
- [ ] **缓存 key**：未使用内置 `hash()`，用 `hash_text`（确定性）。
- [ ] **契约不变**：未修改 `schemas.py` 的 dataclass 字段、`stages.py` 的 StageSpec name/io_schema。
- [ ] **golden fixture 未篡改**：未为让 test 通过而修改 fixture 期望值。
- [ ] **测试覆盖**：新增了对应单元测试，且离线可跑（不依赖真实 LLM）。
- [ ] **stub 已替换**：`stages.py` 中对应 stub 已替换为真实函数。
- [ ] **本地门禁通过**：`ruff check` / `ruff format --check` / `mypy src/` / `pytest tests/ -m "not llm"` 全部通过。

---

## 4. CI 门禁要求

所有 PR 必须通过 CI 三道门禁才能合并（CI 配置见 [`.github/workflows/ci.yml`](.github/workflows/ci.yml)）：

| 门禁 | 命令 | 阶段 |
|---|---|---|
| Lint | `ruff check` + `ruff format --check` | 全阶段 |
| Type check | `mypy src/` | 全阶段 |
| Test | `pytest tests/ -m "not llm"` | 全阶段 |
| Golden test 逐位通过 | 含在 test job 内 | Phase 1+ |
| 评测门禁（`HR@3≥0.6, NDCG@5≥0.4, envy≤2`） | 单独步骤 | Phase 2+（当前注释） |

- `lint` 是门禁之首，`type-check` 与 `test` 均 `needs: lint`，lint 失败即提前终止。
- 需要真实 LLM 的测试标记为 `llm`，CI 默认跳过；手动用 `RUN_LLM_TESTS=1` 触发，报告不阻断合并。
- 完整门禁规则、触发时机与失败行为见 [docs/ci-gates.md](docs/ci-gates.md)。

**禁止跳过 CI 门禁**。如门禁失败，修复实现或发起 spec 变更（见 §6），不得通过禁用测试、修改 fixture 期望值等方式绕过。

---

## 5. spec 变更流程

spec（`spec/` 目录 + 四件套）是唯一真相，不可随意修改。当实现暴露了 spec 的沉默或缺陷时，**改 spec 而非 hack 实现**。

### 5.1 何时发起 spec 变更

- 发现 spec 没说清楚的边界（沉默），且 `spec/05-boundaries.md` 未覆盖。
- 发现 spec 的假设有误（缺陷），导致实现无法正确表达意图。
- 需要修改 `schemas.py` 的 dataclass 字段或 `stages.py` 的 StageSpec name/io_schema。

### 5.2 变更流程

1. **单独的 spec PR**：spec 变更不与实现 PR 混在一起，单独提一个 PR。
2. **PR 描述写明变更理由**：现状是什么、为什么需要改、改后的影响范围。
3. **更新可执行 spec**：若涉及数据契约变化，同步更新 `tests/test_schemas.py` 与 golden fixture；若涉及门禁数值，更新 `config/default.yaml` 与 [docs/ci-gates.md](docs/ci-gates.md)。
4. **spec PR 先于实现 PR 合并**：实现 PR 基于 spec PR 合并后的分支继续。

### 5.3 spec 文件索引

| 文件 | 内容 |
|---|---|
| `spec/00-overview.md` | 项目定位、四件套、架构 |
| `spec/01-schemas.md` | 所有数据契约的字段级 spec |
| `spec/02-stages.md` | 所有 pipeline 阶段的声明 |
| `spec/03-oracles.md` | Oracle 定义：推荐质量 + 互惠公平 |
| `spec/04-fixtures.md` | Fixture 目录与添加规则 |
| `spec/05-boundaries.md` | 显式边界决定：消除 spec 沉默 |

---

## 6. 本地环境

```bash
# 安装开发依赖
pip install -e ".[dev]"

# 跑离线测试（默认，跳过 llm 标记）
pytest tests/

# 跑需要真实 LLM 的测试（手动触发）
RUN_LLM_TESTS=1 pytest tests/ -m "llm"

# Phase 2：安装 FairRec 依赖
pip install -e ".[fair]"
```

有问题先查 [CLAUDE.md](CLAUDE.md)（施工铁律）与 [docs/engineering-plan.md](docs/engineering-plan.md)（施工蓝图）。
