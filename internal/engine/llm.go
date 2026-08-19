package engine

// LLMClient 是 engine 对 LLM 的最小依赖（consumer-defined interface，
// Go 惯用法：消费方定义接口，实现方满足即可）。
//
// Python 基线用鸭子类型 llm_wrapper(messages, **kwargs)；Go 侧收敛为
// **按阶段类型化**的四方法接口：prompt 里的用户文本不可信，路由由
// 调用上下文（阶段函数）决定，不得靠 prompt 内容判别（否则画像含
// "a_to_b" 等标记字样会劫持路由，qodo PR2 #1/#4）。实现
// （internal/signal 替身、internal/bamlllm 桥接）负责按阶段分发、
// 重试 / 缓存 / 传输细节，engine 只拿到文本。
//
// 语义契约：
//   - 任一方法返回错误时，调用方按"该次调用失败"处理
//     （score 阶段 → 未打分保留 embed 权重；introduce → 模板兜底）；
//   - model 为空串表示用实现侧默认模型。
type LLMClient interface {
	// CompleteScore 打分调用（score 阶段，批量 pair prompt）。
	CompleteScore(prompt string, model string) (string, error)
	// CompleteExtract 分节提取调用（extract 阶段）。
	CompleteExtract(prompt string, model string) (string, error)
	// CompleteHyde 假设性描述生成调用（hyde 阶段）。
	CompleteHyde(prompt string, model string) (string, error)
	// CompleteIntroduce 双向话术起草调用（introduce 阶段）。
	CompleteIntroduce(prompt string, model string) (string, error)
}

// NotSpecified 是提取失败 / 未提及分节的填充值
// （spec/01-schemas.md §2）。embed 阶段视其为缺失（零向量）。
const NotSpecified = "Not specified"

// CanonicalSections 是 spec §1/§2 的固定四节词表（spec 沉默 A-1：
// 未参数化进 config，按固定四节处理）。
var CanonicalSections = []string{"skills", "vision", "project", "needs"}
