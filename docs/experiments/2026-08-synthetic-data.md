# 合成数据实验：2026-08 提升推荐质量的多轮迭代

> 目标：站在"用合成数据持续提升 Mutual 作为 LLM harness 的推荐质量"的角度，
> 用子代理（mock LLM API）做标注员/应答模型，多轮实验定位质量瓶颈并落地修复。
> 实验台与全部原始数据在 `lab/`（未入库，可丢弃重跑）；入库产物见 §5。

## 1. 方法

- **实验台** `lab/diag`（Go）：复刻 bench 链路但把信号拆成
  `DirectionalScore`（LLM 替身）与 `EmbedScore`（召回替身）两路，
  支持 blend 权重 / fallback 推荐 / 匹配参数 / 噪声网格。
  官方 seed 偏移下与 `mutual evaluate` 逐位一致（classic 0.875 复现为证）。
- **子代理舰队**（3 波 20 个并行代理）：
  1. 6 个场景作者（互斥陷阱主题：同义改写/词面欺骗/方向不对称/资源竞争/跨域桥/真实语料）；
  2. 8 个标注员（盲标注，不给真值）：全部 member×pool 双向分 0-1；
  3. 4 个 prompt 应答模型（扮演被调用的 LLM，对 4 个契约变体盲评）。
- **盲标注协议**：打分指令= `config recipe.instruction`（引擎真实语义）；
  classic 双标注测一致性（MAE a/b = 0.023/0.040，80 对）——子代理标签可
  当作"真实 LLM 信号"的可信替身。
- **陷阱集 A/B**：12 条陷阱（不可验证宣称/词面堆砌/硬约束/同义改写/
  跨语言/单向吸引/注入/强弱锚点/阶段错配），期望区间双盲（应答代理
  看不到期望值），3 条区间经辩护后修订（虚夸者从真实伙伴处的获益是
  实在的；"愿望强度"≠"现实可得价值"）。

## 2. 核心发现

### F1 词法 surrogate 对黄金对系统性低估（全 8 场景一致）

| 场景 | LLM 信号 HR@3 | 词法管线 HR@3 | 黄金对 MAE(a/b) | 非黄金 MAE(a/b) |
|---|---|---|---|---|
| classic | 1.000 | 0.875 | 0.35/0.55 | 0.04/0.06 |
| drift | 1.000 | 1.000 | 0.36/0.52 | 0.03/0.02 |
| cold | 1.000 | 1.000 | 0.34/0.53 | 0.06/0.11 |
| paraphrase | 1.000 | 0.125 | 0.77/0.65 | 0.10/0.10 |
| decoy | 1.000 | 0.250 | 0.62/0.61 | 0.04/0.05 |
| contention | 1.000 | 1.000 | 0.57/0.65 | 0.07/0.05 |
| messy | 1.000 | 1.000 | 0.75/0.76 | 0.03/0.03 |

**结论**：评测链路（求解器/指标）无缺陷——真实 LLM 信号全场景满分。
离线分数测的是"surrogate 碰巧看见词面"，不是 harness 质量。离线套件的
正确定位：回归检测 + 参数调优；绝对质量靠 prompt 契约（F7）。

### F2 classic 0.875 根因：bench 绕过 blending

`RunScenario` 只喂方向分，config `blending: 0.35/0.65` 从未被执行。
m3↔p3 的 B→A 链路是同义改写（"seeking teams drowning in cloud spend" vs
"devops cicd observability prometheus"），词面零重叠 → NSW 几何均值崩塌
→ p3 容量被 m9 抢占 → m3 零推荐。embed 全画像信号（TF 余弦）本可兜底。
**修复**：`signal.ScoreMatrixBlended` + `ScenarioOptions.EmbedWeight/LLMWeight`
（commit 9ed016b）。

### F3 保底推荐：竞争失利者不应空手而归

推荐列表=匹配边时，PoolBMax 竞争失利者零推荐（classic m3、contention
m03/m06/m07）。`FallbackTopK` 用 PrefMatrix 行首候选补齐：classic 单独
修复至 1.000；decoy 0.250→1.000（NSW 几何均值正确压词面欺骗，保底
恢复可见性）。

### F4 embedW=0.35 被合成数据验证（现行生产值最优）

网格：0.25-0.35 时 contention 1.000 且 envy 最低；≥0.40 词面欺骗敏感性
回升。`config/default.yaml` 现行 0.35/0.65 正是甜点。messy 场景 blend 把
envy 11→2（公平性修复）。

### F5 prompt 契约 A/B：70.8% → 91.7%

| 变体 | 命中率 | 关键修复 |
|---|---|---|
| v0 现行契约 | 70.8% | —— |
| v1 +校准锚点 | 83.3% | vague_both、分数塌缩 |
| v2 +判断纪律 | 87.5% | 词面堆砌 a 方向 0.92→0.10、不可验证 0.78→0.24 |
| v3 +可验证性门+硬约束封顶 | **91.7%** | 硬约束违反 0.05（封顶生效）、注入免疫 |

已落地 `baml_src/score.baml`（commit 9b67b62）。残余失分 2 项均为
"受益方价值"边界争议（受罚者从真实伙伴处获益该给多高分），非能力缺陷。

### F6 匹配参数已近最优

b_max × pool_b_max × noise 网格（48 组合 × 8 场景）：现行 bmax=3/pool=1
在 noise=0.24 下 envy 最低档。paraphrase 类信号盲区任何匹配参数不可救。

## 3. 数据资产

- `data/bench-extended/`（入库）：paraphrase / decoy / messy + notes。
- `lab/scenarios/`（未入库）：asymmetric / contention / bridge（当前词法
  全解，判别力低，留作噪声压力组）。
- `lab/labels/`（未入库）：8 份盲标注（624 对双向分）。
- `lab/traps/`（未入库）：12 陷阱 + 4 变体应答 + 评分器。

## 4. 建议后续

1. surrogate 语义化（同义词归一/K8s↔kubernetes）需 spec 决定：会改
   golden 语义，应作为 ADR 单独评审。
2. `mutual evaluate --extended` CLI 面（当前以 go test 守护）。
3. 双标注协议固化：上新场景时用两个独立 LLM 标注 + MAE<0.05 门槛。
4. 陷阱集扩展至中文为主的画像（引擎真实用户语料）。
