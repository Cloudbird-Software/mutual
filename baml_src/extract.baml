// extract 阶段的 LLM 契约（spec/02-stages.md §1）：
// 自由文本画像 → 四个 canonical section（skills/vision/project/needs）。
//
// 输出为类型化 class——BAML runtime 负责结构化输出解析与校验重试，
// Go 侧不出现 prompt 字符串拼接（languages.yaml llm_prompt 层
// forbidden: prompt_string_interpolation_in_code）。
class ProfileSections {
  skills string @description("What can this person do? Technical/creative capabilities. Use 'Not specified' if not found.")
  vision string @description("What are they passionate about? What drives them? Use 'Not specified' if not found.")
  project string @description("What are they currently working on or want to build? Use 'Not specified' if not found.")
  needs string @description("What are they looking for? What help do they need? Use 'Not specified' if not found.")
}

function ExtractProfile(raw_text: string) -> ProfileSections {
  client "openai/gpt-4o-mini"
  prompt #"
    Extract structured sections from this profile text.

    Profile text:
    {{ raw_text }}

    Extract into these sections (use "Not specified" if not found):
    - skills: What can this person do? What are their technical/creative capabilities?
    - vision: What are they passionate about? What drives them?
    - project: What are they currently working on or want to build?
    - needs: What are they looking for? What help do they need?
  "#
}
