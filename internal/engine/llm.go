package engine

// LLMClient 是 engine 对 LLM 的最小依赖（consumer-defined interface，
// Go 惯用法：消费方定义接口，实现方满足即可）。
//
// Python 基线用鸭子类型 llm_wrapper(messages, **kwargs)；Go 侧收敛为
// 单方法接口：prompt + 可选 model。实现（internal/signals）负责
// 重试 / 缓存 / 传输细节，engine 只拿到文本。
//
// 语义契约：
//   - Complete 返回错误时，调用方按"该次调用失败"处理
//     （score 阶段 → 未打分保留 embed 权重；introduce → 模板兜底）；
//   - model 为空串表示用实现侧默认模型。
type LLMClient interface {
	Complete(prompt string, model string) (string, error)
}

// NotSpecified 是提取失败 / 未提及分节的填充值
// （spec/01-schemas.md §2）。embed 阶段视其为缺失（零向量）。
const NotSpecified = "Not specified"

// CanonicalSections 是 spec §1/§2 的固定四节词表（spec 沉默 A-1：
// 未参数化进 config，按固定四节处理）。
var CanonicalSections = []string{"skills", "vision", "project", "needs"}
