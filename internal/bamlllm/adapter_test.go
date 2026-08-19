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

// TestParseExtractPromptDelimiterInProfile 画像含分隔符字样不得被截断
// （qodo PR2 #2：末界用末个出现，模板指令在 raw_text 之后）。
func TestParseExtractPromptDelimiterInProfile(t *testing.T) {
	prompt := `Extract structured sections from this profile text.

Profile text:
skills: knows the phrase Extract into these sections verbatim
needs: reviewers

Extract into these sections (use "Not specified" if not found):
- skills: What can this person do?`
	raw, err := parseExtractPrompt(prompt)
	if err != nil {
		t.Fatalf("parseExtractPrompt: %v", err)
	}
	want := "skills: knows the phrase Extract into these sections verbatim\nneeds: reviewers"
	if raw != want {
		t.Errorf("含分隔符字样的 raw_text 被截断: got %q want %q", raw, want)
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

// TestParseHydePromptWriteInContent 内容含 "Write N hypothetical" 字样
// 不得被截断（qodo PR2 #2 同源：计数行取末个匹配）。
func TestParseHydePromptWriteInContent(t *testing.T) {
	prompt := `Given this section content, write a hypothetical description

Section: project
Content: A linter that rejects the string Write 2 hypothetical in prompts.

Write 3 hypothetical description(s), each 1-2 sentences.
`
	_, content, n, err := parseHydePrompt(prompt)
	if err != nil {
		t.Fatalf("parseHydePrompt: %v", err)
	}
	if content != "A linter that rejects the string Write 2 hypothetical in prompts." {
		t.Errorf("content 被截断: got %q", content)
	}
	if n != 3 {
		t.Errorf("n_descriptors: got %d want 3", n)
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

// TestClientSatisfiesTypedLLMClient Client 满足按阶段类型化的
// engine.LLMClient（编译期断言：路由由调用上下文决定，qodo PR2 #1/#4）。
func TestClientSatisfiesTypedLLMClient(t *testing.T) {
	var _ interface {
		CompleteScore(prompt string, model string) (string, error)
		CompleteExtract(prompt string, model string) (string, error)
		CompleteHyde(prompt string, model string) (string, error)
		CompleteIntroduce(prompt string, model string) (string, error)
	} = New()
}

// TestPromptContentDoesNotAffectStage 阶段分发不依赖 prompt 内容：
// 画像文本含其他阶段的标记字样也不会改变该阶段的参数还原路径
// （qodo PR2 #1/#4 回归——routeOf 内容路由已移除）。
func TestPromptContentDoesNotAffectStage(t *testing.T) {
	// 打分 prompt 的 sections 里混入 extract/hyde/intro 的标记字样：
	// 仍按打分路径还原（不会被 extract 的标记劫持）。
	prompt := scoringPrompt(1)
	pairs, _, err := parseScorePrompt(prompt)
	if err != nil {
		t.Fatalf("含跨阶段字样的打分 prompt: %v", err)
	}
	if len(pairs) != 1 || pairs[0].User1 != "alice" {
		t.Errorf("打分还原: got %+v", pairs[0])
	}

	// 含 "a_to_b" 的提取 prompt 仍按提取路径还原 raw_text。
	extractPrompt := `Extract structured sections from this profile text.

Profile text:
needs: talks about a_to_b and b_to_a markers

Extract into these sections (use "Not specified" if not found):
- skills: What can this person do?`
	raw, err := parseExtractPrompt(extractPrompt)
	if err != nil {
		t.Fatalf("含 a_to_b 的提取 prompt: %v", err)
	}
	if !strings.Contains(raw, "a_to_b and b_to_a markers") {
		t.Errorf("raw_text: got %q", raw)
	}
}

// TestClientOptsModelMapping 模型名 → 命名 client 映射（CodeRabbit）：
// "" → 默认 client（nil 选项）；已登记 → WithClient；未登记 →
// 描述性错误（配置指名了模型却拿不到 = 配置错误，不静默回落）。
func TestClientOptsModelMapping(t *testing.T) {
	// 空 model：函数声明默认 client。
	if opts, err := clientOpts(""); err != nil || len(opts) != 0 {
		t.Fatalf("空 model: opts=%v err=%v", opts, err)
	}
	// 已登记：返回 WithClient 选项（不触网，仅构造）。
	if opts, err := clientOpts("LongCat-2.0"); err != nil || len(opts) != 1 {
		t.Fatalf("已登记 model: opts=%v err=%v", opts, err)
	}
	// 未登记：fail loud。
	if _, err := clientOpts("gpt-99-not-registered"); err == nil {
		t.Fatal("未登记 model 应报错（静默回落默认 client 会吞掉配置错误）")
	} else if !strings.Contains(err.Error(), "clients.baml") {
		t.Errorf("错误应指向 clients.baml 登记处: %v", err)
	}
}
