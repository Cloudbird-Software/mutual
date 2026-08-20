# CLAUDE.md — Agent 施工指令

> 本文件是 agent 的唯一入口。读它，遵它，不要自行做架构决策。

## 1. 项目本质

Mutual 是一个 **LLM 驱动的双向互惠推荐引擎**。核心范式：**spec 驱动，代码可丢弃**。

- spec（`spec/` 目录 + `internal/domain` 类型 + `config/default.yaml` + `golden/`）是唯一真相。
- 实现代码只是 spec 的执行体，可以随时重写（历史证据：Python 基线 → Go+BAML 重写，golden 逐位对拍后基线移除）。
- **你的工作标准**：实现代码的好坏 = 它是否暴露了 spec 的沉默（没说清楚的边界）与缺陷（错误的假设）。发现沉默时，**改 spec 而非 hack 实现**。

## 2. 施工铁律（不可违反）

1. **先读 spec，再写代码**。开始工作前读 `spec/00-overview.md` 和你要动的 stage 的 spec（`spec/02-stages.md`）。
2. **契约不可改**。`internal/domain` 的类型字段与 `spec/01-schemas.md` 对应；如需修改，先在 spec 文档提出变更理由，经审核后再改。
3. **engine 是纯变换**。`internal/engine` 不碰文件系统、数据库、网络；一切 IO 归 `internal/pipeline`（adapter 层）。分层由 `cmd/archlint` 门禁强制。
4. **golden test 不可绕过**。实现变更必须逐位通过 `internal/goldentest` + 各包 golden 测试。不允许为了让 test 通过而修改 fixture 期望值。
5. **不硬编码参数**。所有可调参数从 `config/default.yaml` 读取。
6. **不引入新依赖**。除非 spec 明确要求。当前 Go 依赖保持最小（见 `go.mod`）。
7. **prompt 契约变更走 BAML**。改 `baml_src/*.baml` → `make baml-generate` → 同步 `golden/baml/` 快照，三者在同一 PR 评审。
8. **holdout/ 对实现/优化 agent 不可见**。禁止阅读 `holdout/` 内容（其 README 除外）；默认 `go test` 自动 skip，仅波次 gate 由人类以 `MUTUAL_HOLDOUT=1` 运行；完整性由 `holdout/manifest_test.go` 常驻 CI 校验（规则见 `docs/workplan-issue7.md` §5.4）。

## 3. 目录结构（Go+BAML 唯一实现面，ADR-0027）

```
mutual/
├── CLAUDE.md / AGENTS.md        # agent 入口 / 仓库级约束索引
├── spec/                        # 唯一真相（00-overview…05-boundaries）
├── config/default.yaml          # 默认配置（参数层）
├── cmd/
│   ├── mutual/                  # CLI（match/query/batch/evaluate/calibrate…）
│   └── archlint/                # 分层依赖门禁
├── internal/
│   ├── domain/                  # 强类型契约（UserID/Matrix/PrefMatrix…）
│   ├── engine/                  # 纯变换核心（11 阶段，Python 语义逐位对齐）
│   ├── pipeline/                # 模式运行器（IO/编排归此层）
│   ├── bamlllm/                 # BAML 类型化客户端 ↔ engine.LLMClient 桥接
│   ├── signal/                  # Embedder/LLM 接口 + 离线替身
│   ├── store/                   # Store + 路径安全
│   ├── num/ rng/                # NumPy 语义浮点 / MT19937
│   ├── bench/ feedback/         # 评测套件 / 反馈校准
│   └── goldentest/              # 跨包 golden 门禁（BAML 快照/注入隔离）
├── baml_src/                    # BAML prompt 契约（唯一事实来源）
├── baml_client/                 # 生成物（提交入库，离线可构建）
├── golden/                      # Python 基线捕获的对拍参考值（engine/ + baml/）
├── docs/                        # AI-GUIDE / ARCHITECTURE / ci-gates…
├── Makefile                     # setup/check 契约（CI-Workflows runtime: go）
└── .github/workflows/ci.yml     # CI（hygiene+check+deps+govulncheck+gate）
```

## 4. 工作流

1. `make check`（vet + archlint + test + evaluate 门禁）全绿才可提 PR。
2. 一个 PR 一件事，diff < 400 行；bug 修复先写复现失败测试。
3. LLM prompt 变更：`baml_src` → `make baml-generate` → `golden/baml/` 同步。
4. 治理：C1 脚手架路径（`.github/`、`Makefile`、`AGENTS.md`、`docs/`）变更的 PR 须引用 ADR-NNNN。

## 5. 历史脚注

Python 基线（`src/mutual/*.py` + `tests/`）在 Go 重写完成、golden 逐位对拍
通过后随 PR3 移除（git 历史可考）。`golden/engine/*.json` 由
`scripts/capture_golden_engine.py`（同随 PR3 移除）在 Python 基线上捕获——
变更 golden 期望值 = 重新证明等价性，须在 PR 中给出捕获依据。
