# messy 场景标注笔记

语料：`lab/scenarios/messy.json`。14 member x 12 pool；m01..m12 <-> p01..p12 黄金；m13/m14 无真值竞争者。
风格：完整英文句子、自然大小写、金额/百分比/年份等具体数字（$4M Series A、96% uptime、38 AMRs）、
工具版本（Postgres 15 / React 18 / Godot 4 / ROS 2）、缩写与俚语（K8s、lol、tbh 风格省略主语）。
每个黄金对刻意用相反语域表达同一意图（正式 RFP 体 vs 小写口语体），词面重叠中等偏低。

## 诊断结果（lab/diag 默认参数，noise=100）

- 干净信号（无噪声）：全部 12 个黄金对均为各自 member 第 1 名，且在所属 pool 列上 NSW 全域最大。
- `go run ./lab/diag diag lab/scenarios/messy.json`：**HR@3=1.000 NDCG@5=1.000**（12/12 全部 hit@1）。
- 最弱干净优势：m11<->p11 dom=+0.098（次优声索者 m09）；其余黄金对 dom >= +0.112。
- 噪声敏感性：noise=0 -> HR@3=1.000；noise=150% -> 0.667；noise=200% -> 0.500（同库 decoy/contention 同幅度退化，非悬崖）。

## 黄金对辩护要点（每对为何是唯一最优、且双向互惠）

| 对 | m 侧获得 | p 侧获得 | 语域反差 |
|---|---|---|---|
| m01<->p01 | Postgres 15 查询计划/索引/autovacuum 治愈月末结算拖慢 | 真实 fintech payments/Go 高负载系统可写案例 + 报酬能力（Series A $4M） | 正式 CTO 公告体 vs 自由职业者口头报价体 |
| m02<->p02 | React Native + HIPAA 架构落地纸面 intake | 诊所运营知识（nurse owner、真实前台流程）换稳定咨询案子 | 温情叙事 vs 中性商务白皮书体 |
| m03<->p03 | multispectral UAV imagery 快速出 NDVI 图层 | 三年 vineyard survey 数据 + 作物问题（ground truth） | 学位论文体 vs 老练外包吆喝体 |
| m04<->p04 | pixel/sprite animation 与 juice 手感（godot 4 可直用） | 有 steam 发布记录和 wishlist 的开发者（milestone 计费客户） | 玩家梗小写体 vs 作品集半正式体 |
| m05<->p05 | scope 3/GHG protocol 合规的 supplier emissions 测量 | 真实 freight/fleet/fuel 数据场验证方法论 | 董事会备忘录体 vs 咨询顾问简介体 |
| m06<->p06 | ROS 2 navigation + fleet 编排调优救 AMR 死锁 | 大规模仓库机器人现场问题（9 库 200 台）证明集成能力 | 运营复盘报告体 vs 一线老师傅吐槽体 |
| m07<->p07 | LLM clause extraction 提升合同审查准确率 | 40,000 份 annotated agreements + attorney 标注规范（稀缺训练资产） | 律所创公文 vs ML 研究员 evals-first 口吻 |
| m08<->p08 | 低功耗 firmware/battery/lora link 疑难修复 | 真实海洋现场（ocean sensor arrays、风暴季回收）做硬件试炼场 | 盐渍野外日志体 vs 嵌入式顾问简介体 |
| m09<->p09 | React 18 + 真 offline sync 救断网课堂 streak | 教师 co-design 学生与真实 classroom 使用数据 | 家长感谢信口吻 vs 友善资深前端体 |
| m10<->p10 | k8s 升级事故后的 migration/autoscaling 稳手修复 | 长期付费产品团队（90 SMB 客户的 SaaS）+ 微服务迁移实案 | 凌晨故障求救小写体 vs 流程化咨询话术 |
| m11<->p11 | box office：seat map/hold rules 替代 google form | 满座 venue、volunteer/patron 社区（真实票务场景） | 社区戏剧宣传腔 vs 独立开发提案体 |
| m12<->p12 | eBPF/ring buffer/kernel 丢事件定位 | 生产级检测管线（SOC detections、false positives 治理）做武器 | 安全团队周报体 vs 内核老炮狂言体 |

唯一性来源：每对的 0.6 主项（needs_m∩skills_p 与 needs_p∩skills_m）共享 3-5 个硬桥接词，
而这些内容词不进入其他任何 pool 的 skills/needs（跨领域天然低碰撞已用 export 验证：
非黄金对的 member->pool 最高仅 0.122，出现在 m09->p11，见下"干扰者"）。

## 干扰者硬伤（为什么表面相似仍不得分）

- **m13（MLOps 救火队，最危险干扰者）**：skills 明写 `kubernetes, terraform, triton, vllm, cuda profiling`，
  与 p10 全称 `Kubernetes/k8s 家族` 词面撞得最狠；但 p10 要的是稳定的 founder-led 产品团队长期托管，
  m13 是项目制救火——反向互惠近零（clean rl≈0.01）。对 p07 表面同属 LLM 圈，但 p07 要 annotated agreements
  与 attorney 标注反馈，m13 给不了；对 p03 同属 GPU 视觉管线词汇，但 p03 只收 vineyard survey 问题。无真值。
- **m14（pre-seed 非技术创始人）**：满嘴融资黑话（$750k pre-seed、equity、11% reply rate），钱味数字与
  d01/d05/d11 的 $4M/$18k 数字符噪声同型；但没有任何一方需要的技能入项（needs↔skills 双向几乎零交集，
  多个 pool 上 NSW=0）。制造资源竞争与数字噪声。无真值。
- **m09->p11（clean lr=0.122）**：共享 `offline` 等词（教室平板断网 vs 剧场 offline check-in pads），
  但剧场要的是 ticketing 行业流（box office/seat map/hold），教师给不了票务工程，价值错配为弱单向。
  （已把 p11 的 lobby wifi 改写降耦，保留这一路作可见压力源。）
- **相邻域混淆内置**：p01 数据库调优 vs p05 碳核算同属企业报告语域但技能不相交；
  p10 集群运维 vs p12 eBPF 内核同属底层系统词场但需求方向相反（hosting vs kernel events）；
  p03 无人机影像 vs p06 仓库机器人同属感知/编队词场但 m06 不产多光谱数据。
- 全部干扰者共同点：至多单向弱重叠（多为 project/vision 层 0.2 权重词或数字噪声），
  双向 0.6 主项必有硬伤（阶段不符：seed/pre-seed vs Series A；规模不符： solo demo vs 240 卡车队；
  方向相反：卖技能 vs 找团队给不了对方所需的另一半）。

## 同义改写陷阱清单

1. **主打陷阱：m10 写 `k8s`、p10 写 `kubernetes`**——两个词在整个语料中从不互换出现。
   它是该黄金对 A->B 方向 0.6 主项的中心词，词面重叠被人为清零，只能靠次级回声
   （migration / autoscaling / pods + 反向 docker compose、hosting）补足。任何只做词面匹配的打分器会失血，
   能把 K8s 对齐到 Kubernetes 的语义打分器才会拿到奖励。
2. lora 在 m08/p08 两侧拼写一致（`lora link`），而 lorawan 未出现——陷阱留给把二者混用的嵌入器同时惩罚双方。
3. 版本号噪声：`Postgres 15`、`React 18`、`Godot 4`、`ROS 2` 分词后产生裸数字 token（15/18/4/2），
   与 `38 AMRs`、`scope 1-3`、`ATT&CK` 等形成无数无害碰撞，考验打分器不被数字牵着走。
4. 黄金对内部大量零字面同义对：reconciliation service crawls <-> tuning queries native language；
   paper intake <-> workflow chaos consulting；therapy visits <-> clinic intake rescues；
   buoy battery dies <-> milliwatts like rent money / sleep-state acupuncture；
   Detection agents shed 0.4% kernel events <-> ring buffer sizing sampling alchemy。

## 复现

```
cd D:\Projects\mutual
go run ./lab/diag diag lab/scenarios/messy.json   # HR@3=1.000 NDCG@5=1.000 envy=11
go run ./lab/diag export lab/scenarios/messy.json out.json  # 全 pair 无噪双信号
```
