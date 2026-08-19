// Prompt 模板解析：config 内联 > 外部文件 > 内置默认
// （与 Python 基线 resolve_prompt_templates 语义一致）。
//
// 自定义模板的显式契约（qodo PR2 #3/#4）：四类模板各有一组**结构
// 标记**（见 requiredTemplateMarkers），是 BAML 桥接（internal/bamlllm）
// 从渲染后 prompt 还原类型化参数的依据。自定义模板改写/删除标记
// 会让每次 LLM 调用静默降级，因此加载时即校验——缺标记 = 描述性
// 配置错误（fail loud），而非运行期逐调用失败。
package config

import (
	"fmt"
	"os"
	"strings"
)

// 模板名（固定四类，spec/02-stages.md 的 LLM 阶段契约）。
const (
	TemplateScoring      = "scoring"
	TemplateIntroduction = "introduction"
	TemplateSection      = "section"
	TemplateHyde         = "hyde"
)

// 内置默认模板（与 Python 基线逐字符一致——prompt 变更受 BAML-1
// golden 门禁约束，此处为未配置时的兜底）。
const defaultScoringPrompt = `You are a matchmaking expert. Score the potential connection between two people.

Person A (user1):
{user1_sections}

Person B (user2):
{user2_sections}

Instruction: {instruction}

Score from two directions:
1. How valuable is this connection for Person A? (A→B score, 0.0-1.0)
2. How valuable is this connection for Person B? (B→A score, 0.0-1.0)

Respond in JSON:
{{"a_to_b": <float>, "b_to_a": <float>, "reasoning": "<brief>"}}
`

const defaultIntroPrompt = `Write a personalized introduction for a matched pair.

Person A: {user1_name}
{user1_sections}

Person B: {user2_name}
{user2_sections}

Write two paragraphs:
- "For {user1_name}: ..." explaining why they should connect with Person B.
- "For {user2_name}: ..." explaining why they should connect with Person A.

Also suggest 2-3 starter topics for their first conversation.
`

const defaultSectionPrompt = `Extract structured sections from this profile text.

Profile text:
{raw_text}

Extract into these sections (use "Not specified" if not found):
- skills: What can this person do? What are their technical/creative capabilities?
- vision: What are they passionate about? What drives them?
- project: What are they currently working on or want to build?
- needs: What are they looking for? What help do they need?

Respond in JSON:
{{"skills": "...", "vision": "...", "project": "...", "needs": "..."}}
`

const defaultHydePrompt = `Given this section content, write a hypothetical description
that would semantically match people who should connect with this person.

Section: {section_name}
Content: {section_content}

Write {n_descriptors} hypothetical description(s), each 1-2 sentences.
`

// requiredTemplateMarkers 各类模板必须包含的结构标记（BAML 桥接的
// 参数还原契约 + 打分响应解析契约）。自定义模板可改措辞，但必须
// 保留这些标记（占位符 {…} 亦是标记的一部分，渲染前原样存在）。
var requiredTemplateMarkers = map[string][]string{
	// "a_to_b"：打分响应的 JSON 键约定（engine.parseScoringResponse
	// 与 spec/04-fixtures.md §7.1 fake 路由都依赖它出现在 prompt 中）。
	TemplateScoring: {"Person A (user1):", "Person B (user2):", "Instruction:", "a_to_b"},
	TemplateSection: {"Profile text:", "Extract into these sections"},
	TemplateHyde:    {"Section:", "Content:", "Write {n_descriptors} hypothetical"},
	// 话术默认模板无 Instruction 行，Instruction 为可选标记。
	TemplateIntroduction: {"Person A:", "Person B:"},
}

// ValidatePromptTemplate 校验自定义模板保留全部结构标记；缺标记
// 返回描述性错误（fail loud，qodo PR2 #3）。
func ValidatePromptTemplate(name, text string) error {
	for _, marker := range requiredTemplateMarkers[name] {
		if !strings.Contains(text, marker) {
			return fmt.Errorf(
				"自定义 %s prompt 模板缺少结构标记 %q：标记是 LLM 桥接从渲染后 prompt 还原类型化参数的依据，"+
					"缺失会导致该阶段每次调用静默降级；请保留标记（可改其余措辞）或移除自定义模板沿用默认",
				name, marker)
		}
	}
	return nil
}

// ResolvePromptTemplates 解析四类 prompt 模板。
//
// 优先级：config 内联（prompts.{name}_prompt_text）> 外部文件
// （promptPaths[name]）> 内置默认。返回的 map 恒含全部四键。
// 自定义模板（内联或文件）缺结构标记 → 描述性错误（#3）。
func (c *Config) ResolvePromptTemplates(promptPaths map[string]string) (map[string]string, error) {
	prompts := mmap(mmap(c.raw["prompts"]))
	defaults := map[string]string{
		TemplateScoring:      defaultScoringPrompt,
		TemplateIntroduction: defaultIntroPrompt,
		TemplateSection:      defaultSectionPrompt,
		TemplateHyde:         defaultHydePrompt,
	}
	out := make(map[string]string, len(defaults))
	for name, def := range defaults {
		if inline, ok := prompts[name+"_prompt_text"].(string); ok && inline != "" {
			if err := ValidatePromptTemplate(name, inline); err != nil {
				return nil, err
			}
			out[name] = inline
			continue
		}
		if path, ok := promptPaths[name]; ok && path != "" {
			// 显式指定的 prompt 文件读取失败 = 配置错误（fail loud）。
			// 静默回落内置默认会让 LLM 用非预期模板产出漂移分数，
			// 且排查线索为零（CodeRabbit）。
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("prompt 模板 %s 读取失败（path=%s）: %w", name, path, err)
			}
			if err := ValidatePromptTemplate(name, string(data)); err != nil {
				return nil, err
			}
			out[name] = string(data)
			continue
		}
		out[name] = def
	}
	return out, nil
}
