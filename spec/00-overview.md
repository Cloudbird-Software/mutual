# Mutual Spec — 总览

> **Spec 即真相，代码可丢弃。** 代码只是 spec 沉默或缺陷的产物。
> 本目录是唯一真相源。实现代码（`src/`）可以随时重写，但 spec 不可随意修改。

## 1. 项目定位

Mutual 是一个 **LLM 驱动的双向互惠推荐引擎**，用于协会会员互识、商业机会推荐、招商与投资对接。核心特征：

- **双向互惠**：A 推荐 B 必须同时满足 B 也愿意对接 A，是"匹配"而非"排序"。
- **自然语言丛林**：实体只有自由文本画像（简历、需求、愿景、项目），无确定属性标签。LLM 是唯一能做语义判断的组件。
- **Spec 驱动**：strongDM 范式——契约是唯一真相，实现代码可丢弃重写。
- **开发期零真实数据**：全部用 benchmark 与合成 fixture，不接触真实协会数据。

## 2. 四件套（唯一真相）

| 件 | 文件 | 作用 |
|---|---|---|
| **schema** | `src/mutual/schemas.py` + `spec/01-schemas.md` | IO 契约：每个数据结构的字段、类型、语义 |
| **stage** | `src/mutual/stages.py` + `spec/02-stages.md` | 变换声明：每阶段输入/输出/纯函数/run·load·dump |
| **config** | `config/default.yaml` | 可调参数：blending、budget、degree、prompt |
| **golden** | `tests/golden/` + `spec/04-fixtures.md` | 可执行 spec：固定输入→固定输出，实现重写后逐位通过 |

## 3. 三层漏斗 + 互惠求解

```
召回（embedding 语义初筛，全量低成本）
  → 精排（LLM 双向打分 A→B / B→A，预算上限）
    → 匹配（NSW 全局一致 + envy 公平性，确定性可复现）
```

- 召回层：用 embedding 把全量 N×N 降到可承受的候选对数。
- 精排层：LLM 对候选对做双向语义打分，方向性不盲目对称化。
- 匹配层：把 LLM 分数落入 `PrefMatrix`，交给 NSW 求解器做全局互惠最优。

## 4. 评测闭环

双指标离线可算，无需真实用户：

| 指标 | 来源 | 衡量维度 |
|---|---|---|
| HR@1/3/5、NDCG@5 | AgentRecBench | 推荐质量：该推荐的有没有被推荐 |
| envy-freeness | FairRec | 互惠公平：双方是否都受益 |

评测通过标准写入 spec（如 `HR@3 ≥ 0.6 且 envy ≤ 2`），CI 门禁强制执行。

## 5. Spec 文件索引

| 文件 | 内容 |
|---|---|
| `00-overview.md` | 本文件：项目定位、四件套、架构 |
| `01-schemas.md` | 所有数据契约的字段级 spec |
| `02-stages.md` | 所有 pipeline 阶段的声明 |
| `03-oracles.md` | Oracle 定义：推荐质量 + 互惠公平 |
| `04-fixtures.md` | Fixture 目录与添加规则 |
| `05-boundaries.md` | 显式边界决定：消除 spec 沉默 |

## 6. 复用来源

| 资产 | 来源 | 复用内容 |
|---|---|---|
| spec 骨架 | Choreo | schemas/stages/golden/config 四件套结构 |
| 评测 oracle | AgentRecBench | HR@1/3/5、NDCG@5、Simulator 评测循环 |
| 互惠求解 | FairRec | nsw_maximize、check_envy、合成市场生成器 |
| agent 模块 | AgentRecBench | Reasoning/Memory/Planning/ToolUse 接口 |
| 多 agent 范式 | AgenticRec | v2 演进参考，v1 不引入 |
