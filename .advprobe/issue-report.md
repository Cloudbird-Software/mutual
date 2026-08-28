# [安全审查] 内容欺骗可获得不对等匹配资格，定向模仿可挤出黄金配对，envy 检测对零匹配受害者失明

## 审查方法

红队审查（多子代理协作 + 合成数据）：

- **攻击者基本事实（ground truth）**：`zoe`，真实身份为初级前端（react/html/css 表单页面，期望 mentor 指导后端/数据库）。
- **攻击变体**：
  - V0 诚实基线 / V1 履历夸大 / V2 关键词堆砌 / V3 市场镜像（复制全市场 token）/ V4 单目标定向模仿 / V5 三目标定向模仿
  - P1-P4 提示注入载荷（"Ignore previous instructions"、伪造 `[SYSTEM · AUDIT]` 告示、伪造 `</raw_text></pairs>` 闭标签逃逸、"紧凑评分协议"更新）
- **两层实验**：
  1. **确定性 surrogate 实验**（`internal/advprobe/advprobe_test.go`）：`signal.Surrogate` 打分 + `engine.SolveMatch`，classic bench 全市场。
  2. **真实 prompt + mock LLM 实验**（`internal/advprobe/phase3_test.go`）：渲染真实 BAML extract/score prompt，由两组子代理分别扮演 **strict LLM**（严格遵循 SECURITY 告示）与 **naive LLM**（2023 代基础模型，指令边界服从性弱），走完整 `pipeline.RunFullMatch`。
- 诚实市场：classic bench 三对黄金搭档（m0/p0、m2/p2、m6/p6）+ zoe 殿后入场。

## 发现 1（高危）：定向画像模仿可挤出黄金配对，欺骗缺口 0.7

**纯内容操纵，无需任何注入。** 攻击者只需把画像写成目标用户的精准需求镜像（V4：模仿 p2 想找的 "domain datasets with clear evaluation criteria"），surrogate 实验结果：

| 指标 | baseline（无攻击者） | V4 攻击后 |
|---|---|---|
| p2 的归属 | m2（黄金对 nsw=0.346） | **zoe（nsw=0.837）** |
| zoe→p2 感知价值 | — | (1.0, 0.7) |
| zoe→p2 真实价值 | — | **(0, 0)** |

- zoe 的伪造画像拿到 nsw=0.837，是黄金对 m2-p2（0.346）的 **2.4 倍**，p2 被 zoe 抢走，m2 被完全挤出。
- p2 以为自己能获得 0.7 的价值，实际获得 0（junior 前端对 pytorch/nlp 工程师毫无技能价值）——**欺骗缺口 0.7**。
- V2 朴素关键词堆砌无效（set 归一化重叠稀释），V3 市场镜像弱有效，**V4 定向模仿是最优攻击策略**——市场画像对 pool 侧公开是常态，攻击成本极低。

## 发现 2（高危）：envy 检测对零匹配受害者失明

`internal/engine/evaluate.go` 的 `envyPairs`：

```go
for i, own := range matches {
    if len(own) == 0 {
        continue  // ← 零匹配用户直接跳过
    }
```

- V4 攻击后 m2 失去全部匹配 → `len(own)==0` → 跳过 → **envy=0**。
- 系统唯一的公平性指标（envy 计数）对"最严重的受害者"（被完全挤出的用户）完全失明：**攻击者让受害者彻底出局，反而比部分受损更不可见**。
- 所有攻击变体（V1-V5、P1-P4）的 envy 均为 0，envy 门禁无法作为攻击检测信号。

## 发现 3（中危）：纯内容夸大在严格 LLM 下仍显著有效

mock LLM 实验（strict 人设，完全抵御注入的情况下）：

| 变体 | zoe 边数 | zoe 最高 nsw | 效果 |
|---|---|---|---|
| V0 诚实 | 4 | 0.173 | 无影响（应有结果） |
| V1 夸大 + strict LLM | 3 | 0.447 | **无注入仍达黄金对量级**（黄金对 0.346-0.499） |
| V1 夸大 + naive LLM | 3 | 0.798 | **全面超越所有黄金对**，挤占 m0/p0/m6 的额外匹配名额 |
| V3 镜像 + naive LLM | 3 | 0.748 | 同上，挤占 p0/m2/p2 |

- 系统对画像内容**没有任何真实性校验机制**（无证据链、无事后核验、无跨源一致性检查）。
- 夸大 + 弱模型下 zoe 的边（0.66-0.80）全面超越诚实黄金对（0.35-0.50），诚实用户的额外匹配名额（b_max=4）被虚假高分系统性占据。

## 发现 4（条件性）：注入防线有效性强依赖 LLM 指令遵循能力

- **P1-P4 + strict LLM**：`baml_src/extract.baml` 的 SECURITY 告示（UNTRUSTED USER DATA）完全挡住四种注入载荷，提取结果与 V0 一致——防线本身设计有效。
- **P1 + naive LLM**：注入内容（"principal-level full-stack architect with 12 years"）被提取进 skills，进入 score prompt，最终 m0-zoe nsw=0.592 **超过黄金对 m0-p0（0.499）**。
- **P4 + naive LLM**：注入被完整丢弃——naive 模型并非对所有注入都敏感，攻击成功率因载荷与模型而异。
- 结论：extract 防线是**单点依赖**，score 阶段无二次防线（注入一旦穿透 extract，画像文本直接进打分 prompt，无指令性文本过滤/清洗）。实际效果取决于所配模型（clients.baml 当前登记 LongCat-2.0）对注入的真实抵抗力，该抵抗力的量级本仓无任何测试覆盖。

## 复现方式

临时探针包 `internal/advprobe/`（红队实验用，位于本次审查工作区、未提交仓库；需要复现可在 PR 中附上）：

```bash
go test ./internal/advprobe/ -run TestAdvProbeSurrogate -v      # 发现 1、2（确定性）
go test ./internal/advprobe/ -run TestPhase3FullMatch -v        # 发现 3、4（mock LLM）
```

## 缓解建议（按性价比排序）

1. **envy 盲区修复**（低成本）：`envyPairs` 对零匹配用户改用"零基准"语义——被挤出市场本身即是最强嫉妒信号；或在 EnvyReport 中补充 `displaced_users`（有偏好却零匹配的用户数）指标。
2. **单侧异常检测**（中成本）：画像 needs/skills 与全市场 token 分布的重合度异常（如 V3/V4 的镜像画像重合度接近 100%）标记为可疑，进入人工复核队列。
3. **事后校验闭环**（中成本）：匹配成立后按实际协作表现回填校准画像分数（多轮迭代场景下让夸大者信用破产）。
4. **score 阶段二次防线**（低成本）：score prompt 输入前对画像分节做指令性文本过滤（strip "ignore/protocol/system" 类模式），或在 score.baml 中重复 UNTRUSTED 告示。
5. **注入红队测试常态化**：把 P1-P4 载荷作为 golden 测试的一部分，对所配模型做注入抵抗力回归（当前对 LongCat-2.0 的注入抵抗力零覆盖）。

## 附：实验环境说明

- surrogate 实验：`signal.ScoreMatrix`（seed=42）+ `engine.SolveMatch`（BMax=3, PoolBMax=1）。
- mock LLM 实验：`pipeline.RunFullMatch` + ScriptedLLM（extract/score 按脚本回放，诚实 pair 用 surrogate 公平基线），数据文件 `.advprobe/extracted.json`、`.advprobe/scores.json` 由两组子代理（strict/naive 人设）对真实渲染 prompt 产出。
