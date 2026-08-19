package config

import (
	"strings"
	"testing"
)

// TestDefaultTemplatesSatisfyMarkers 内置默认模板满足各自的结构
// 标记约束（默认值与校验规则未来漂移时在此暴露）。
func TestDefaultTemplatesSatisfyMarkers(t *testing.T) {
	for name, def := range map[string]string{
		TemplateScoring:      defaultScoringPrompt,
		TemplateIntroduction: defaultIntroPrompt,
		TemplateSection:      defaultSectionPrompt,
		TemplateHyde:         defaultHydePrompt,
	} {
		if err := ValidatePromptTemplate(name, def); err != nil {
			t.Errorf("默认 %s 模板应满足标记约束: %v", name, err)
		}
	}
}

// TestResolvePromptTemplatesDefault 无自定义配置 → 全部四键为内置默认。
func TestResolvePromptTemplatesDefault(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	templates, err := cfg.ResolvePromptTemplates(nil)
	if err != nil {
		t.Fatalf("ResolvePromptTemplates: %v", err)
	}
	if templates[TemplateScoring] != defaultScoringPrompt ||
		templates[TemplateHyde] != defaultHydePrompt {
		t.Error("未配置时应回落内置默认模板")
	}
}

// TestResolvePromptTemplatesRejectsMarkerlessCustom 自定义模板缺结构
// 标记 → 加载期描述性错误（fail loud，qodo PR2 #3：而非运行期每次
// LLM 调用静默降级）。
func TestResolvePromptTemplatesRejectsMarkerlessCustom(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	// 通过 raw 注入内联自定义模板（模拟 prompts.scoring_prompt_text）。
	cfg.raw["prompts"] = map[string]any{
		"scoring_prompt_text": "Just rate these two people however you like.",
	}
	_, err = cfg.ResolvePromptTemplates(nil)
	if err == nil {
		t.Fatal("缺标记的自定义打分模板应报错")
	}
	if !strings.Contains(err.Error(), "scoring") || !strings.Contains(err.Error(), "标记") {
		t.Errorf("错误应指明模板名与缺失标记: %v", err)
	}
}

// TestResolvePromptTemplatesAcceptsCustomWithMarkers 保留全部结构标记的
// 自定义模板合法（可改措辞，标记是唯一硬约束）。
func TestResolvePromptTemplatesAcceptsCustomWithMarkers(t *testing.T) {
	cfg, err := Default()
	if err != nil {
		t.Fatalf("Default: %v", err)
	}
	custom := "Custom scoring words.\n\nPerson A (user1):\n{user1_sections}\n\n" +
		"Person B (user2):\n{user2_sections}\n\nInstruction: {instruction}\n\n" +
		"Respond in JSON: {{\"a_to_b\": <float>, \"b_to_a\": <float>}}\n"
	cfg.raw["prompts"] = map[string]any{"scoring_prompt_text": custom}
	templates, err := cfg.ResolvePromptTemplates(nil)
	if err != nil {
		t.Fatalf("保留标记的自定义模板应通过: %v", err)
	}
	if templates[TemplateScoring] != custom {
		t.Error("自定义模板应优先生效")
	}
}
