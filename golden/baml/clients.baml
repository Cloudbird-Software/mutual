// 命名 client 注册表（CodeRabbit：使 config models.pair_llm 生效）。
//
// 函数声明的 client "openai/gpt-4o-mini" 是离线单测默认；生产调用由
// internal/bamlllm/adapter.go 把 config 的模型名映射为命名 client 并经
// baml.WithClient 传入——模型选择由此成为版本化 prompt 契约的一部分，
// 无法映射的模型名 fail loud（不静默回落默认 client）。
//
// API key 经环境变量注入（OPENAI_API_KEY），与 config/models 的约定一致。
client Longcat {
  provider openai
  options {
    model "LongCat-2.0"
    base_url env.LONGCAT_BASE_URL
  }
}
