# Changelog

本文件记录对外可见的变更。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [Unreleased]

### Added
- config / internal/domain：原生 Go fuzz 目标（FuzzParseYAML / FuzzHashText / FuzzPyJSONDumpSections，
  Scorecard Fuzzing=0 → 自愈；mutual #5）。
- 初始模板工程（CI gate / hygiene / dependabot / automerge 全套护栏）。
### Fixed
- config：YAML 子集解析器空键（冒号行与冒号加值形态）静默产出空键 map 而非报错——fuzz 发现并修复。

