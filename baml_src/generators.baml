// BAML 生成器配置（llm_prompt 层，ADR-0027）。
//
// 生成物 baml_client/ 提交入库（离线可构建）；prompt 变更必须过
// BAML-1 golden test（internal/goldentest/baml_snapshot_test.go 对
// baml_src/*.baml 做快照比对——改 prompt 而不改 golden = 门禁阻断）。
//
// 再生成：npx @boundaryml/baml@0.226.1 generate（版本须与下方 version 一致）。
generator go {
  output_type "go"
  output_dir "../baml_client"
  version "0.226.1"
  client_package_name "github.com/Cloudbird-Software/mutual/baml_client"
  on_generate "gofmt -w ."
}
