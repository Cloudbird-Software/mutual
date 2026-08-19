# CI 门禁定义

> 本文件定义 Mutual 的 CI 门禁规则：每道门禁的命令、触发时机与失败行为。
> CI pipeline 实现见 [`.github/workflows/ci.yml`](../.github/workflows/ci.yml)。
> 门禁数值来源见 [`spec/03-oracles.md`](../spec/03-oracles.md)。
> PR3 起 Go+BAML 为唯一实现面（ADR-0027），Python 面门禁随基线移除。

---

## 1. 门禁总览

| # | 门禁 | 命令/实现 | 触发时机 | 失败行为 |
|---|---|---|---|---|
| 1 | hygiene | CI-Workflows 复用工作流 | 每次 push / PR | 阻断合并 |
| 2 | check | `make check`（vet + archlint + test + evaluate） | 每次 push / PR | 阻断合并 |
| 3 | deps（PR 面） | dependency-review（base↔head 比对） | 每次 PR | 阻断合并 |
| 4 | deps-audit（push 面） | `govulncheck ./...` | 每次 push | 阻断 |
| 5 | adr-required | C1 脚手架变更须引用 ADR-NNNN | 每次 PR | 阻断合并 |
| 6 | gate | needs 汇总判定 | 总是 | 任一未绿即阻断 |

---

## 2. 门禁详解

### 2.1 check（runtime: go 复用工作流）

- **命令**：`make setup` + `make check`（CI-Workflows check.yml，go-version 1.24）。
- **构成**：
  - `make lint` = `go vet ./...`（编译器级静态检查）；
  - `make arch` = `go run ./cmd/archlint`（分层依赖门禁：engine 纯变换、IO 归 pipeline；新 internal 包未登记即违规，fail-closed）；
  - `make test` = `go test ./...`（含 golden 差分对拍 + BAML 快照/注入隔离回归）；
  - `make evaluate` = 离线评测门禁（HR@3≥0.6 / NDCG@5≥0.4 / total_envy≤2，数值在 `config/default.yaml`）。
- **失败行为**：阻断合并。

### 2.2 golden 差分对拍（含在 test 内）

- Go 引擎输出与 `golden/engine/full_flow.json`（Python 基线捕获）逐位比对；
- `baml_src/*.baml` 与 `golden/baml/` 快照逐字节一致；
- BAML 不可信字段必须 `|text_block` 隔离 + UNTRUSTED USER DATA 指令（`TestBAMLUntrustedDataIsolation`）。
- **失败行为**：阻断合并；修改期望值 = 重新证明等价性，须在 PR 给出捕获依据。

### 2.3 deps / deps-audit（供应链）

- **PR 面**：dependency-review 比对 base↔head 的新增依赖（许可证 + 漏洞）。
- **push 面**：直推 main 无 base↔head 可比，用 `govulncheck ./...`（OSV 可达性分析，版本钉 v1.1.4）兜底——防止"跳过=通过"的 gate 语义漏洞（ADR-0021 红队 #11-C）。
- **失败行为**：阻断。

### 2.4 adr-required（C1 治理）

- C1 脚手架路径（`.github/`、`AGENTS.md`、`Makefile`、`docs/`、`zizmor.yml` 等）变更的 PR 标题/描述须引用 `ADR-NNNN`，且该 ADR 须真实存在于 agent-registry/decisions（幽灵 ADR 拦截）。
- **失败行为**：阻断合并。

### 2.5 gate（汇总）

- `needs: [hygiene, check, deps, deps-audit, adr-required]`，`if: always()`；
- 显式 `permissions: {}`（零权限，CodeRabbit）；
- 任一 required job 非 success/skipped → 阻断。

---

## 3. 真实 LLM 验证（非阻断）

CI 全离线（fake 替身 + golden）。需要真实 LLM 的验证手动执行：

```bash
export OPENAI_API_KEY=...   # 或 MUTUAL_API_KEY
go run ./cmd/mutual match --config config/default.yaml --input <cohort>
```

结果人工评审，不阻断合并（离线门禁已覆盖语义正确性）。
