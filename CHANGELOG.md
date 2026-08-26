# Changelog

本文件记录对外可见的变更。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [Unreleased]

### Added
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
- docs：合成数据实验报告（docs/experiments/2026-08-synthetic-data.md）。
- config / internal/domain：原生 Go fuzz 目标（FuzzParseYAML / FuzzHashText / FuzzPyJSONDumpSections，
  Scorecard Fuzzing=0 → 自愈；mutual #5）。
- 初始模板工程（CI gate / hygiene / dependabot / automerge 全套护栏）。
### Fixed
- config：YAML 子集解析器空键（冒号行与冒号加值形态）静默产出空键 map 而非报错——fuzz 发现并修复。


