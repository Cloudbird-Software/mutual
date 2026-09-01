# Changelog

本文件记录对外可见的变更。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [Unreleased]

### Added
- training：高价值小模型训练工具包（scripts/training/ + docs/training/）——
  P0 Score 双向打分器微调（prepare_data.py 数据准备 / train_reranker.py 训练 /
  evaluate_reranker.py 门禁评测 / serve_reranker.py 本地推理服务）、P1 Embedding
  替换实测（bench_embedding.py，bge-m3 等开源候选）、P2 Extract 四节微调
  （train_extract.py 导出 LLaMA-Factory 数据集）。规格 docs/training/TRAINING_SPEC.md、
  开源借鉴 docs/training/OPENSOURCE_REFERENCES.md、接入引擎 docs/training/MODEL_SERVING.md。
  仅合成+公开数据训练，不动引擎 Go 代码；训练产物 scripts/training/output/ 入 .gitignore。
- engine：硬约束资格判定（EligibilityExclusions）——显式声明的硬约束
  （"hard constraint/硬约束" 标记门控，地理实体/本地驻场两族）遇
  counterpart 可见违反自述 → 该对前置于候选选择排除（不进 LLM 精排、
  不耗预算，MR-8 守护 honored）。fail-safe：无显式声明不触发、无可见
  违反正据不排除（交 LLM 层）。config matching.hard_constraint_filter
  默认开启（golden 语料无约束标记，零行为差异）；bench ScenarioOptions
  同步支持（constraints 扩展场景：关闭过滤时违反者排序倒挂，开启后
  黄金对回到 top-1）。
- pipeline：RunBatchMatch 二部全链路 e2e 测试（extract→hyde→embed→
  similarity→select→score→solve，替身确定性）+ 资格过滤端到端断言。
- cli：mutual evaluate --extended——扩展陷阱套件诊断输出（生产姿态：
  blending 取配置值 + FallbackTopK + 资格过滤；不计入门禁）。
- bench：zh_assoc 中文语料入扩展套件（去克隆 v2：唯一 vision/相位/
  需求措辞——初版同 vision 克隆使评测随噪声在 0.43-1.00 跳变，已在
  生成器层修复）。CJK 修复后标准噪声实测 HR@3 1.000/0.867/0.767
  （对照纯 ASCII 切词 0.00-0.33）。
- metamorphic：夜间大规模回归（build tag nightly，make nightly）——
  Go 原生三领域语料生成器，同集 400 / 二部 300×450 的 MR 套件 +
  求解确定性/耗时守卫；日常 CI 零影响。
- internal/metamorphic：蜕变测试套件常驻 CI——8 条 MR（唯一噪声不变性/
  重复不变性/泛化降级/堆砌反超率测量/干扰者不偷位/克隆确定性/已知值
  阶梯/排除对 honored），六场景 + 中文语料全绿；LLM 冷上下文校准变异
  算子语义有效（三元组 8/8+8/8、阶梯严格单调、方向独立）。
- signal：Tokenize 追加 CJK 字符二元组——跨语言画像离线可观测性
  （海外商人↔本地企业域中文画像词法全盲修复，三领域中文语料
  HR@3 0.00-0.33 → 1.000；英文路径逐位不变）。
- config：预算随规模上调——每人对 24→48、全局 1200→4800（200+ 人场景
  黄金对召回覆盖 75%→93.5%，端到端 HR@3 0.560→1.000；含缩放律注释）。
- domain：EvaluationReport.EnvyRate() 人均 envy 度量（total_envy 随
  O(N²) 增长，绝对门禁只对小场景有意义）。
- bench：扩展陷阱套件 data/bench-extended（paraphrase 同义改写 / decoy 词面
  欺骗 / messy 真实语料公平性）+ RunExtendedScenario/LoadExtendedScenario
  （白名单 fail-closed）。
- bench/signal：ScenarioOptions 新增双信号混合（ScoreMatrixBlended，镜像
  config blending；classic 同义改写盲区 HR@3 0.875→1.000）与保底推荐
  FallbackTopK（PoolBMax 竞争失利者用 PrefMatrix 行首候选补齐；decoy
  0.250→1.000）。零值路径 golden 逐位不变。
- baml：ScorePairs 打分契约升级——新增判断纪律（语义等价零词面重叠仍计直接匹配、
  词面复述无实质不计分、可验证性门、硬约束违反单向封顶 0.1、阶段/规模错配降档、
  双向独立打分）与校准锚点（0.0-1.0 五档全距使用）。合成陷阱集 A/B（12 陷阱 ×
  盲评）：契约命中率 70.8% → 91.7%，硬约束/词面堆砌/不可验证宣称全部修正。
- docs：合成数据实验报告 v3（docs/experiments/2026-08-synthetic-data.md，
  三轮迭代：契约 A/B / 三领域规模压测 / 蜕变测试与饱和判定）。
- config / internal/domain：原生 Go fuzz 目标（FuzzParseYAML / FuzzHashText / FuzzPyJSONDumpSections，
  Scorecard Fuzzing=0 → 自愈；mutual #5）。
- 初始模板工程（CI gate / hygiene / dependabot / automerge 全套护栏）。
### Fixed
- domain：UserID 契约统一为白名单 `^[A-Za-z0-9][A-Za-z0-9._-]*$` + 长度 ≤64 +
  禁 `..`/`__`（domain.ValidUserID 单一真源；红队 RT2 #43 / RT3 #51/#53/#55/#58）。
  注册层与存储/解析层校验分裂被夹缝字符利用：空格 ID 使打分块头不可反解析
  （整批 unscored 绕过 verifiability gate）、冒号/URL 等内容型 ID 借平台话术
  投递钓鱼载体、单个夹缝 ID 使带 Store 的全员批次 DoS、`__` ID 构造 PairID
  碰撞（诚实对静默蒸发/连带封禁）。pipeline 入口（RunFullMatch/RunQueryMatch）
  对直接构造的 Profile fail-loud 拒绝，先于任何 LLM/embed 花费。
- engine：硬约束资格过滤否定盲区（红队 RT3 #49 场景 A，中危·误杀
  合法配对）——violates() 的子串匹配对否定语境失明，合法成员自述
  "坚持大陆实体运营，杜绝纯远程交付"因子串 "纯远程" 被误判违规、
  静默砍掉合法 pair。修复：命中词所在子句的前半段含否定语（杜绝/
  不做/没有/not/never…，中英双语，英文按词元匹配）→ 视为否定式
  自述不算可见违反，交 LLM 层（fail-safe 方向：无证据不排除）。
- engine：召回融合负权重分母放大（红队 RT3 #54，高危）——"缺失分节
  从分母豁免"在负权重分节上可被选择性填充利用：攻击者只填交叉对齐的
  负权重分节、留空正权重分节，分母被压到 0.60 而分子保留 0.80 交叉项，
  融合分 4/3 放大突破余弦值域，对诚实用户获 100%+ 不对等召回优势并
  劫持 LLM 预算名额。修复：缺失 term 按零相似计入分母（留空不再豁免
  稀释）+ 融合输出 [0,1] 值域 clamp；双侧全有效（golden 语料）时逐位
  不变。spec/05-boundaries.md §1 语义同步更新。
- engine：extract 路径 raw_text 渲染缺失标记中和（红队 RT3 #52，中危）——
  formatProfileRawText 不经 NeutralizePromptMarkers 直接拼接，画像标记行
  与非规范分节名（"Instruction:"、"### Pair 9"）原样进入提取 LLM，
  scoring/intro 与 extract 防线不一致（extract 是唯一缺失确定性中和层的
  用户可控输入路径）。修复：raw_text 整体行级中和，与 FormatSections
  同款防线；删除死代码 FormatRawText。golden 语料（无标记行）逐字节不变。
- engine：硬约束资格门非确定性裁决（红队 RT3 #57，低危）——
  DetectHardConstraint/violates 对 sections map 无序遍历，多约束声明时
  规则族逐次运行随机翻转、跨分节违反词拼接顺序随机，同一输入的资格
  裁决不可复现（违反确定性契约）。修复：分节名排序遍历/排序拼接。
- domain：画像分节长度上限（红队 RT3 #50，中危·财务 DoS）——
  Profile.Sections 无长度上限，单个超大分节（实测 2MB → 2.96MB
  prompt）逐字进入该成员参与的每个 LLM/embedding 调用，放大平台
  成本与延迟。修复：MaxSectionLen=8000 runes 单一真源
  （domain.OverlongSection），注册层（ProfileFromMap）/构造层
  （NewProfile）/pipeline 入口（RunFullMatch 直接构造 Profile 与
  RunQueryMatch 的 query 文本）全线 fail-loud 拒绝，先于任何
  LLM/embed 花费。
- config：YAML 子集解析器空键（冒号行与冒号加值形态）静默产出空键 map 而非报错——fuzz 发现并修复。


