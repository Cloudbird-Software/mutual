// hyde 阶段的 LLM 契约（spec/02-stages.md §2）：
// 为每个 section 生成假设性描述（Hypothetical Document Embeddings），
// 用于 embedding 召回的语义扩展（方向性：描述"应与谁连接"的画像）。
function GenerateHypothetical(
  section_name: string,
  section_content: string,
  n_descriptors: int
) -> string[] {
  client "openai/gpt-4o-mini"
  prompt #"
    Given this section content, write a hypothetical description
    that would semantically match people who should connect with this person.

    Section: {{ section_name }}
    Content: {{ section_content }}

    Write {{ n_descriptors }} hypothetical description(s), each 1-2 sentences.
  "#
}
