# 贡献指南（CONTRIBUTING）

> 感谢参与 Mutual。本文件规定 PR 规则与开发流程，所有贡献者必须遵守。
> 施工铁律见 [CLAUDE.md](CLAUDE.md)，CI 门禁定义见 [docs/ci-gates.md](docs/ci-gates.md)。

---

## 1. 开发流程

Mutual 是 spec 驱动项目，**先读 spec，再写代码**（Go+BAML 唯一实现面，ADR-0027）：

```
读 spec → 实现/修改 → 写测试 → make check → 提 PR
```

### 1.1 读 spec

每次开始工作前，至少读完：

- `spec/00-overview.md` — 项目定位、四件套、架构
- 你要动的 stage 对应的 `spec/02-stages.md §<N>`
- `spec/05-boundaries.md` — 相关边界决定（消除 spec 沉默）
- 导航：[docs/AI-GUIDE.md](docs/AI-GUIDE.md)（改哪/怎么改/铁律）

### 1.2 实现约定

- **engine 是纯变换**：`internal/engine` 不碰文件系统、数据库、网络；一切 IO 归 `internal/pipeline`。分层由 `cmd/archlint` 门禁强制（新 internal 包必须登记进 layerOf，fail-closed）。
- **强类型契约**：数据契约在 `internal/domain`（与 `spec/01-schemas.md` 对应），LLM prompt 契约在 `baml_src/*.baml`（类型化输入/输出，BAML runtime 负责结构化解析与校验重试）。
- 不硬编码参数，一律从 `config/default.yaml` 读取。
- 不引入 spec 未要求的新依赖（当前依赖见 [go.mod](go.mod)）。
- 缓存/种子只用确定性哈希（`domain.HashText`），不用语言运行时随机源。

### 1.3 写测试

为新增实现写对应的单元测试，与实现同包（`*_test.go`）。测试是 spec 的可执行断言。

- golden test（`internal/goldentest` + 各包 golden 测试）守护固定输入→固定输出，**不允许为了让 test 通过而修改 fixture 期望值**。
- BAML prompt 契约由 `golden/baml/` 快照 + 注入隔离回归（`TestBAMLUntrustedDataIsolation`）守护。
- 需要真实 LLM 的验证走 `cmd/mutual` CLI 手动执行，CI 全离线。

### 1.4 BAML prompt 变更

改 `baml_src/*.baml` → `make baml-generate` 重生成 `baml_client/` → `cp baml_src/*.baml golden/baml/` 同步快照，三者在同一 PR 评审（快照门禁会拦截漏同步）。

### 1.5 提 PR

本地验证通过后提交 PR：

```bash
# 本地跑 CI 等价门禁（vet + archlint + test + evaluate）
make check
```

---

## 2. PR 规则

### 2.1 一个 PR 一件事

- diff < 400 行；bug 修复先写复现失败测试。
- 提交信息用 Conventional Commits；对外接口变更写 `CHANGELOG.md`。
- 治理：C1 脚手架路径（`.github/`、`Makefile`、`AGENTS.md`、`docs/`）变更的 PR 须引用 ADR-NNNN（CI 强制存在性校验）。

### 2.2 PR 必须包含

1. 实现代码（`internal/<pkg>` 或 `cmd/`）
2. 对应的单元测试
3. 如有 spec 变更，**单独的 spec PR**（不与实现混在一起）

### 2.3 PR 描述模板

```markdown
## 变更点
<简述>

## Spec 引用
spec/02-stages.md §<N> / spec/05-boundaries.md §<N>

## 边界处理
<列出相关边界及实现如何处理>

## 测试
<列出新增测试及覆盖的场景>

## Breaking changes
<如有契约或行为变更，明确写出；无则写"无">
```

---

## 3. Review checklist

Review 时逐项检查（Reviewer 与作者自查都用这份）：

- [ ] **spec 一致**：实现与 `spec/02-stages.md` 的输入/输出/语义一致。
- [ ] **边界处理**：`spec/05-boundaries.md` 相关边界都已正确处理，没有 hack 绕过。
- [ ] **纯变换**：`internal/engine` 无 IO，IO 全在 pipeline 层；`cmd/archlint` 通过。
- [ ] **无硬编码参数**：所有可调参数从 `config/default.yaml` 读取。
- [ ] **无新依赖**：未引入 spec 未要求的依赖。
- [ ] **契约不变**：未修改 `internal/domain` 类型字段；如改了，走 spec 变更流程。
- [ ] **golden fixture 未篡改**：未为让 test 通过而修改 fixture 期望值。
- [ ] **BAML 快照同步**：`baml_src` 变更与 `baml_client/`、`golden/baml/` 三者一致。
- [ ] **测试覆盖**：新增了对应单元测试，且离线可跑。
- [ ] **本地门禁通过**：`make check` 全绿。

---

## 4. CI 门禁要求

所有 PR 必须通过 CI 门禁才能合并（CI 配置见 [`.github/workflows/ci.yml`](.github/workflows/ci.yml)）：

| 门禁 | 命令 | 说明 |
|---|---|---|
| hygiene / adr-required | 复用工作流 | 仓库卫生 + C1 脚手架 ADR 引用 |
| check | `make check`（vet + archlint + test + evaluate） | runtime: go 复用工作流 |
| deps（PR 面） | dependency-review | base↔head 依赖比对 |
| deps-audit（push 面） | `govulncheck ./...` | OSV 可达性分析，零容忍 |
| gate | 汇总判定 | 任一 required 未绿即阻断 |

评测门禁数值：`HR@3≥0.6, NDCG@5≥0.4, total_envy≤2`（`config/default.yaml`，spec/03-oracles.md）。

**禁止跳过 CI 门禁**。如门禁失败，修复实现或发起 spec 变更（见 §5），不得通过禁用测试、修改 fixture 期望值等方式绕过。

---

## 5. spec 变更流程

spec（`spec/` 目录）是唯一真相，不可随意修改。当实现暴露了 spec 的沉默或缺陷时，**改 spec 而非 hack 实现**。

### 5.1 何时发起 spec 变更

- 发现 spec 没说清楚的边界（沉默），且 `spec/05-boundaries.md` 未覆盖。
- 发现 spec 的假设有误（缺陷），导致实现无法正确表达意图。
- 需要修改 `internal/domain` 类型字段或阶段 IO 契约。

### 5.2 变更流程

1. **单独的 spec PR**：spec 变更不与实现 PR 混在一起，单独提一个 PR。
2. **PR 描述写明变更理由**：现状是什么、为什么需要改、改后的影响范围。
3. **更新可执行 spec**：若涉及数据契约变化，同步更新对应测试与 golden fixture；若涉及门禁数值，更新 `config/default.yaml` 与 [docs/ci-gates.md](docs/ci-gates.md)。
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
# 安装依赖（Go 1.24+）
make setup

# 跑离线全量门禁
make check

# 只跑测试
make test

# 重生成 BAML 客户端（prompt 契约变更时）
make baml-generate
```

有问题先查 [CLAUDE.md](CLAUDE.md)（施工铁律）与 [docs/AI-GUIDE.md](docs/AI-GUIDE.md)（导航）。
