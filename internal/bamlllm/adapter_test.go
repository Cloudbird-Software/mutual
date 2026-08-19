package bamlllm

import (
	"strings"
	"testing"
)

// 打分 prompt 构造与 engine.buildScoringPrompt + 默认模板同构
// （config/templates.go defaultScoringPrompt 渲染结果）。
func scoringPrompt(batchSize int) string {
	template := `You are a matchmaking expert. Score the potential connection between two people.

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
	render := func(u1, u2, s1, s2 string) string {
		r := strings.NewReplacer(
			"{user1_sections}", s1, "{user2_sections}", s2,
			"{instruction}", "Score this match on the value of connecting these two people.",
		)
		return r.Replace(template)
	}
	var blocks []string
	if batchSize >= 1 {
		blocks = append(blocks, "### Pair 1: (alice, bob)\n"+render("a", "b", "go, web", "rust, ml"))
	}
	if batchSize >= 2 {
		blocks = append(blocks, "### Pair 2: (carol, david)\n"+render("c", "d", "art", "writing"))
	}
	if batchSize == 1 {
		return blocks[0]
	}
	header := "Score each of the 2 pairs below, in both directions. Respond ONLY with a JSON array."
	return header + "\n\n" + strings.Join(blocks, "\n\n")
}

// TestParseScorePrompt 批量与单对 prompt 均能还原 pair 输入与 instruction。
func TestParseScorePrompt(t *testing.T) {
	for _, size := range []int{1, 2} {
		pairs, instr, err := parseScorePrompt(scoringPrompt(size))
		if err != nil {
			t.Fatalf("size=%d: %v", size, err)
		}
		if len(pairs) != size {
			t.Fatalf("size=%d: 还原 %d 对", size, len(pairs))
		}
		if pairs[0].User1 != "alice" || pairs[0].User2 != "bob" {
			t.Errorf("pair id: got (%s, %s)", pairs[0].User1, pairs[0].User2)
		}
		if pairs[0].User1_sections != "go, web" || pairs[0].User2_sections != "rust, ml" {
			t.Errorf("sections: got (%q, %q)", pairs[0].User1_sections, pairs[0].User2_sections)
		}
		if !strings.HasPrefix(instr, "Score this match") {
			t.Errorf("instruction 截取错误: %q", instr)
		}
	}
}

// TestParseScorePromptRejectsCustomTemplate 自定义模板缺标记 → 错误
// （engine 按调用失败处理，不静默错配）。
func TestParseScorePromptRejectsCustomTemplate(t *testing.T) {
	if _, _, err := parseScorePrompt("rate these folks somehow"); err == nil {
		t.Error("缺 ### Pair 标记应报错")
	}
	// 有 Pair 标记但块内无 Person A/B 标记 → 同样报错。
	prompt := "### Pair 1: (a, b)\nJust some custom text without markers"
	if _, _, err := parseScorePrompt(prompt); err == nil {
		t.Error("块内缺 Person A/B 标记应报错")
	}
}

// TestParseExtractPrompt 提取 prompt 的 raw_text 还原。
func TestParseExtractPrompt(t *testing.T) {
	prompt := `Extract structured sections from this profile text.

Profile text:
skills: go
needs: reviewers

Extract into these sections (use "Not specified" if not found):
- skills: What can this person do?`
	raw, err := parseExtractPrompt(prompt)
	if err != nil {
		t.Fatalf("parseExtractPrompt: %v", err)
	}
	if raw != "skills: go\nneeds: reviewers" {
		t.Errorf("raw_text: got %q", raw)
	}
}

// TestParseHydePrompt HyDE prompt 的分节名/内容/数量还原。
func TestParseHydePrompt(t *testing.T) {
	prompt := `Given this section content, write a hypothetical description
that would semantically match people who should connect with this person.

Section: needs
Content: Looking for Go reviewers for my parser project.

Write 3 hypothetical description(s), each 1-2 sentences.
`
	name, content, n, err := parseHydePrompt(prompt)
	if err != nil {
		t.Fatalf("parseHydePrompt: %v", err)
	}
	if name != "needs" {
		t.Errorf("section: got %q", name)
	}
	if content != "Looking for Go reviewers for my parser project." {
		t.Errorf("content: got %q", content)
	}
	if n != 3 {
		t.Errorf("n_descriptors: got %d", n)
	}
}

// TestParseIntroPrompt 话术 prompt 的双方姓名/sections 还原
// （默认模板无 Instruction 行 → instruction 为空串）。
func TestParseIntroPrompt(t *testing.T) {
	prompt := `Write a personalized introduction for a matched pair.

Person A: alice
skills: go
needs: reviewers

Person B: bob
skills: rust

Write two paragraphs:
- "For alice: ..." explaining why they should connect with Person B.
`
	u1, s1, u2, s2, instr, err := parseIntroPrompt(prompt)
	if err != nil {
		t.Fatalf("parseIntroPrompt: %v", err)
	}
	if u1 != "alice" || u2 != "bob" {
		t.Errorf("names: got (%q, %q)", u1, u2)
	}
	if s1 != "skills: go\nneeds: reviewers" {
		t.Errorf("s1: got %q", s1)
	}
	if s2 != "skills: rust" {
		t.Errorf("s2: got %q", s2)
	}
	if instr != "" {
		t.Errorf("默认模板无 instruction: got %q", instr)
	}
}

// TestRouteRoutingRules 路由优先级：a_to_b > Profile text > hyde > intro。
func TestRouteRoutingRules(t *testing.T) {
	c := New()
	cases := []struct {
		prompt string
		want   string
	}{
		{"respond with a_to_b score", "score"},
		{"Profile text: skills: go", "extract"},
		{"write a hypothetical description for this section", "hyde"},
		{"Write a personalized introduction", "intro"},
	}
	for _, tc := range cases {
		// 直接断言路由判定（不触发真实 LLM 调用）。
		got := routeOf(tc.prompt)
		if got != tc.want {
			t.Errorf("route(%q): got %s want %s", tc.prompt, got, tc.want)
		}
	}
	_ = c
}
