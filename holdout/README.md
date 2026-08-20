# holdout/ — Holdout 测试套件（冻结件）

> 作者：holdout 作者（非实现/非优化执行者）。规则见 `docs/workplan-issue7.md` §5.4。

## 本目录的纪律

1. **实现与优化 agent 禁止阅读本目录内容。** 本 README 是允许被读的唯一文件。
2. 功能测试（`mt_hidden_test.go`、`scenarios_test.go`）默认 `t.Skip`；
   仅在波次 gate 时由人类以 `MUTUAL_HOLDOUT=1 go test ./holdout/` 运行。
3. `manifest_test.go` **不解锁也跑**（常驻 CI 的防篡改校验）。
4. gate 运行时，实现/优化 agent 只能看到 pass/fail 汇总与计数，失败详情只对人类可见。
5. 除 `api.go` 的 `Default` 接线点外，本目录所有文件冻结：内容哈希登记在
   `manifest.json`；任何改动（含接线、manifest 重生成）需人类 owner 批准
   （治理措施见 Cloudbird-Software/.github#104）。

## 内容

- `mt_hidden_test.go`：MT11–MT15 隐藏 metamorphic 变体
  （实现者只知道 issue #7 的 MT1–MT10）。
- `scenarios/HT-01..12.json` + `scenarios_test.go`：12 个人工编写的业务陷阱场景。
- `manifest.json` + `manifest_test.go`：冻结文件的 sha256 清单与常驻校验。
- `api.go`：Harness 接口与 WorldResult；`Default` 是唯一接线点。
