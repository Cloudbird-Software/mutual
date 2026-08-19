// Prompt 模板解析：config 内联 > 外部文件 > 内置默认
// （与 Python 基线 resolve_prompt_templates 语义一致）。
package config

import (
	"os"
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

// ResolvePromptTemplates 解析四类 prompt 模板。
//
// 优先级：config 内联（prompts.{name}_prompt_text）> 外部文件
// （promptPaths[name]）> 内置默认。返回的 map 恒含全部四键。
func (c *Config) ResolvePromptTemplates(promptPaths map[string]string) map[string]string {
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
			out[name] = inline
			continue
		}
		if path, ok := promptPaths[name]; ok && path != "" {
			if data, err := os.ReadFile(path); err == nil {
				out[name] = string(data)
				continue
			}
		}
		out[name] = def
	}
	return out
}
