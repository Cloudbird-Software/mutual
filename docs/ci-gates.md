# CI 门禁定义

> 本文件定义 Mutual 的 CI 门禁规则：每道门禁的命令、触发时机与失败行为。
> CI pipeline 实现见 [`.github/workflows/ci.yml`](../.github/workflows/ci.yml)。
> 门禁数值来源见 [`spec/03-oracles.md`](../spec/03-oracles.md)。

---

## 1. 门禁总览

| # | 门禁 | 命令 | 触发时机 | 失败行为 | 阶段 |
|---|---|---|---|---|---|
| 1 | Lint | `ruff check src tests` | 每次 push / PR | 阻断合并 | 全阶段 |
| 2 | Format | `ruff format --check src tests` | 每次 push / PR | 阻断合并 | 全阶段 |
| 3 | Type check | `mypy src/` | 每次 push / PR | 阻断合并 | 全阶段 |
| 4 | Test | `pytest tests/ -m "not llm" --tb=short` | 每次 push / PR | 阻断合并 | 全阶段 |
| 5 | Golden test | 含在 Test 门禁内（`tests/test_golden.py`） | 每次 push / PR | 阻断合并 | Phase 1+ |
| 6 | 评测门禁 | 评测脚本 + gate 断言 | 每次 push / PR | 阻断合并 | Phase 2+（当前注释） |
| 7 | LLM 测试 | `RUN_LLM_TESTS=1 pytest tests/ -m "llm"` | 手动 / 每周 | 报告，**不阻断** | 全阶段 |

---

## 2. 门禁详解

### 2.1 Lint（门禁之首）

- **命令**：`ruff check src tests`
- **配置**：`pyproject.toml [tool.ruff]`，`line-length=100`，`target-version="py310"`。
- **触发时机**：每次 push 到 main、每次 pull_request。
- **失败行为**：阻断合并。`lint` 是 CI 的第一个 job，`type-check` 与 `test` 均 `needs: lint`，lint 失败则后续 job 不运行，提前暴露格式/语法问题。

### 2.2 Format check

- **命令**：`ruff format --check src tests`
- **触发时机**：同 Lint，在 `lint` job 内紧随 `ruff check` 执行。
- **失败行为**：阻断合并。本地用 `ruff format src tests` 自动修复。

### 2.3 Type check

- **命令**：`mypy src/`
- **配置**：`pyproject.toml [tool.mypy]`，`python_version="3.10"`，`strict=false`，`warn_unused_ignores=true`。
- **触发时机**：每次 push / PR，`needs: lint`。
- **失败行为**：阻断合并。
- **说明**：`strict=false` 是 Phase 1 骨架期的渐进策略，随实现成熟逐步收紧；`warn_unused_ignores=true` 确保遗留的 `# type: ignore` 不会沉淀。

### 2.4 Test（默认跳过 LLM）

- **命令**：`pytest tests/ -m "not llm" --tb=short`
- **配置**：`pyproject.toml [tool.pytest.ini_options]`，`testpaths=["tests"]`，`markers=["llm: 需要真实 LLM 的测试（默认跳过）"]`。
- **触发时机**：每次 push / PR，`needs: lint`。
- **失败行为**：阻断合并。
- **说明**：标记为 `llm` 的测试默认跳过，CI 不消耗真实 LLM 配额；用 `RUN_LLM_TESTS=1` 手动触发（见 §3）。

### 2.5 Golden test（逐位回归）

- **命令**：含在 Test 门禁内，由 `tests/test_golden.py` 执行。
- **触发时机**：每次 push / PR（Phase 1+）。
- **失败行为**：阻断合并。
- **铁律**：实现重写后必须**逐位**通过 golden test。**不允许为了让 test 通过而修改 fixture 期望值**（`tests/golden/` 下的固定答案）。若 fixture 本身需变更，走 spec 变更流程（见 [CONTRIBUTING.md §5](../CONTRIBUTING.md)）。
- **断言分层**（[spec/05-boundaries.md §11](../spec/05-boundaries.md)）：Phase 1 只断言算法无关不变量（边数/度分布/方向性/确定性/fallback）；NSW 求解器相关的期望（`envy_report`、`market_30x20`）与移植数值统计（`final_weights`/`embedding_scores`）分别在 Phase 2 激活 / 重新固化。
- **离线执行**：golden test 用 `fake_llm` + `fake_embedder`（确定性契约见 [spec/04-fixtures.md §7](../spec/04-fixtures.md)），不依赖真实 LLM，保证 CI 可复现。

### 2.6 评测门禁（Phase 2+）

- **命令**：
  ```bash
  python -m mutual.cli evaluate \
    --config config/default.yaml \
    --fail-on-gate
  ```
- **实现**：`src/mutual/bench.py`（评测套件）+ `src/mutual/cli.py`（`evaluate` 子命令）。
  纯离线、确定性，不依赖真实 LLM/embedder，CI 无需 API 凭据即运行。
  套件 = 三场景 bench（`data/bench/{classic,drift,cold}.json`，强模型标注画像 +
  黄金真值；信号源 `src/mutual/surrogate.py`，推荐列表**源自求解器输出**，
  求解器退化直接压低 HR/NDCG——由 `tests/test_evalloop.py::TestGateDiscrimination`
  回归守护）+ 合成市场（构造性 oracle，贡献 envy 信号）。真实 LLM 链路验证见
  `tests/test_llm_online.py`（`RUN_LLM_TESTS=1`，报告不阻断）。
- **门禁数值**：

  | 指标 | 阈值 | 来源 |
  |---|---|---|
  | `HR@3` | `>= 0.6` | [spec/03-oracles.md §1.3](../spec/03-oracles.md) |
  | `NDCG@5` | `>= 0.4` | [spec/03-oracles.md §1.3](../spec/03-oracles.md) |
  | `total_envy`（left + right） | `<= 2` | [spec/03-oracles.md §2.2](../spec/03-oracles.md) |

- **触发时机**：Phase 2+ 每次 push / PR（当前已启用，见 [`.github/workflows/ci.yml`](../.github/workflows/ci.yml)）。
- **失败行为**：阻断合并。代码实现改版必须回归通过，否则视为 spec 违约。
- **数值同步**：门禁数值同时写入 `config/default.yaml`（`evaluation.gates`），由 `EvaluationReport.passes_gates()` 读取；本文件与 spec/03-oracles.md 为权威定义，config 为执行参数。

  ```yaml
  # config/default.yaml
  evaluation:
    gates:
      hr_at_3_min: 0.6
      ndcg_at_5_min: 0.4
      total_envy_max: 2
  ```

### 2.7 LLM 测试（可选，不阻断）

- **命令**：`RUN_LLM_TESTS=1 pytest tests/ -m "llm" --tb=short`
- **触发时机**：手动触发，或每周定时（未来可加 `schedule` 触发器）。CI 默认不跑。
- **失败行为**：**报告，不阻断合并**。用于监控真实 LLM 行为漂移，不影响常规开发节奏。
- **前提**：需配置 `OPENAI_API_KEY`。

---

## 3. `RUN_LLM_TESTS=1` 使用方式

默认情况下，所有标记为 `llm` 的测试被跳过（CI 与本地均如此）。需要真实 LLM 的测试用 `@pytest.mark.llm` 标记。

### 3.1 本地手动触发

```bash
# 只跑 llm 标记的测试
RUN_LLM_TESTS=1 pytest tests/ -m "llm" --tb=short

# 同时跑离线与在线测试
RUN_LLM_TESTS=1 pytest tests/ --tb=short
```

> 注意：仅设 `RUN_LLM_TESTS=1` 但未配置 `OPENAI_API_KEY` 时，涉及真实调用的测试会因鉴权失败而报错。请确保环境变量就绪。

### 3.2 CI 中启用（可选）

在 [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) 的 `test` job 中保留了一段注释步骤：

```yaml
# - name: LLM tests (optional, non-blocking)
#   if: env.RUN_LLM_TESTS == '1'
#   env:
#     RUN_LLM_TESTS: ${{ secrets.RUN_LLM_TESTS }}
#     OPENAI_API_KEY: ${{ secrets.OPENAI_API_KEY }}
#   run: pytest tests/ -m "llm" --tb=short || true
```

取消注释并在仓库 Secrets 中配置 `RUN_LLM_TESTS=1` 与 `OPENAI_API_KEY` 即可启用。末尾 `|| true` 确保该步骤**不阻断合并**。

### 3.3 标记规则

- 需要真实 LLM API 的测试必须加 `@pytest.mark.llm`。
- 离线测试用 `fake_llm` / `fake_embedder` fixture（见 `tests/conftest.py`），**不加** `llm` 标记，保证 CI 默认可跑。
- `pyproject.toml` 已注册该 marker，未注册的 marker 会触发 `PytestUnknownMarkWarning`。

---

## 4. 门禁与阶段对照

| 阶段 | 启用的门禁 | 验收标准 |
|---|---|---|
| Phase 0 | Lint / Format / Type check / Test（schema + stage 注册测试） | `pytest tests/test_schemas.py tests/test_stages.py` 通过 |
| Phase 1 | + Golden test 逐位通过 | `pytest tests/ -m "not llm"` 全部通过 |
| Phase 2 | + 评测门禁 | `pytest tests/` 全部通过；评测门禁通过 |
| Phase 3 | 无新增门禁 | 互惠 bench 三场景跑通；LLM 自改进反馈闭环演示 |

详见 [docs/engineering-plan.md §8](engineering-plan.md)。

---

## 5. 门禁失败的处理原则

1. **不绕过门禁**：禁止通过禁用测试、注释掉门禁步骤、修改 golden fixture 期望值等方式让 CI 变绿。
2. **先定位根因**：门禁失败通常是实现暴露了 spec 的沉默或缺陷。
3. **改实现或改 spec**：
   - 实现问题 → 修复实现。
   - spec 沉默/缺陷 → 走 spec 变更流程（[CONTRIBUTING.md §5](../CONTRIBUTING.md)），单独提 spec PR。
4. **门禁数值调整**：若需调整评测门禁阈值，必须先更新 [spec/03-oracles.md](../spec/03-oracles.md) 与 `config/default.yaml`，再同步本文件。
