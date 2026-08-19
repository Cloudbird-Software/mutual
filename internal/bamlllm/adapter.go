// Package bamlllm 是 BAML 类型化客户端到 engine.LLMClient 的桥接层。
//
// 为什么需要桥接：engine 的阶段函数（extract/hyde/score/introduce）
// 以字符串 prompt 为契约（Python 基线逐位对齐，golden 门禁依赖）；
// BAML 生成客户端是类型化的。本包把 engine 发出的**确定性 prompt
// 结构**（默认模板的标记，受 BAML-1 golden 快照约束）还原成类型化
// 输入，调用 BAML 函数，再把类型化结果序列化回 engine 各解析器
// 期望的 JSON 形状。
//
// 分发契约（qodo PR2 #1/#4）：engine.LLMClient 按阶段类型化分发
// （CompleteScore/CompleteExtract/CompleteHyde/CompleteIntroduce），
// 路由由调用上下文决定——prompt 里插值的用户文本不可信，不得作为
// 阶段判别器（否则画像含 "a_to_b" 字样会劫持路由）。
//
// 参数还原依赖默认模板的结构标记（"### Pair N:"、"Profile text:"
// 等）。自定义模板必须保留这些标记：config.ResolvePromptTemplates
// 在加载时校验（fail loud），标记缺失 = 配置错误，而非静默降级。
//
// 失败语义：prompt 结构还原失败时返回错误——engine 按"该次调用
// 失败"处理（score → 未打分保留 embed 权重；introduce → 模板兜底），
// 不中断 pipeline（spec/05-boundaries.md §3）。
package bamlllm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	baml "github.com/Cloudbird-Software/mutual/baml_client/baml_client"
	"github.com/Cloudbird-Software/mutual/baml_client/baml_client/types"
)

// Client 实现 engine.LLMClient：按阶段类型化的字符串 prompt →
// BAML 类型化调用。
//
// model 参数被忽略：模型选择由 baml_src/*.baml 的 client 声明
// （版本化 prompt 契约的一部分），不由调用点散落指定。
type Client struct {
	// Timeout 单次 BAML 调用（底层是网络 LLM 请求）的时限；
	// 0 = defaultTimeout。engine 的降级路径只覆盖"返回 error"，
	// 挂起只能靠超时切断（CodeRabbit）。
	Timeout time.Duration
}

// defaultTimeout 单次 LLM 调用默认上限。
const defaultTimeout = 120 * time.Second

// New 返回 BAML 桥接客户端。
func New() *Client { return &Client{} }

// ctx 派生带超时的调用 context（四条路由共用）。
func (c *Client) ctx() (context.Context, context.CancelFunc) {
	d := c.Timeout
	if d <= 0 {
		d = defaultTimeout
	}
	return context.WithTimeout(context.Background(), d)
}

// CompleteScore 打分路径：还原批量 pair 输入 → ScorePairs →
// JSON 数组（单对 → JSON 对象，与 engine.parseScoringResponse 对齐）。
func (c *Client) CompleteScore(prompt string, model string) (string, error) {
	_ = model
	return c.routeScore(prompt)
}

// CompleteExtract 提取路径：还原 raw_text → ExtractProfile →
// {"skills":...,"vision":...,"project":...,"needs":...}。
func (c *Client) CompleteExtract(prompt string, model string) (string, error) {
	_ = model
	return c.routeExtract(prompt)
}

// CompleteHyde HyDE 路径：还原分节输入 → GenerateHypothetical →
// JSON 字符串数组（engine.parseDescriptors 的首选解析形状）。
func (c *Client) CompleteHyde(prompt string, model string) (string, error) {
	_ = model
	return c.routeHyde(prompt)
}

// CompleteIntroduce 话术路径：还原双方画像 → DraftIntroduction →
// {"intro":...,"starter_topics":...}（engine.parseIntroResponse 形状）。
func (c *Client) CompleteIntroduce(prompt string, model string) (string, error) {
	_ = model
	return c.routeIntroduce(prompt)
}

// routeScore 打分路径：还原批量 pair 输入 → ScorePairs →
// JSON 数组（单对 → JSON 对象，与 engine.parseScoringResponse 对齐）。
//
// 结果按 user1/user2 标识对齐请求顺序（CodeRabbit）：engine 侧按位置
// 消费，BAML 返回乱序/缺失/多余都会错配分数。数量或标识不匹配 →
// 整批返回错误，engine 按"该次调用失败"处理（batch 内候选对全部
// 记 unscored，保留 embed 权重，不静默错配）。
func (c *Client) routeScore(prompt string) (string, error) {
	pairs, instruction, err := parseScorePrompt(prompt)
	if err != nil {
		return "", err
	}
	ctx, cancel := c.ctx()
	defer cancel()
	scores, err := baml.ScorePairs(ctx, pairs, instruction)
	if err != nil {
		return "", fmt.Errorf("baml ScorePairs: %w", err)
	}
	aligned, err := alignScoresByID(pairs, scores)
	if err != nil {
		return "", err
	}
	if len(aligned) == 1 {
		out, err := json.Marshal(scoreJSON{
			AToB: aligned[0].A_to_b, BToA: aligned[0].B_to_a,
			Reasoning: aligned[0].Reasoning,
		})
		return string(out), err
	}
	arr := make([]scoreJSON, 0, len(aligned))
	for _, s := range aligned {
		arr = append(arr, scoreJSON{AToB: s.A_to_b, BToA: s.B_to_a, Reasoning: s.Reasoning})
	}
	out, err := json.Marshal(arr)
	return string(out), err
}

// alignScoresByID 把 BAML 返回的打分结果按 (user1, user2) 标识对齐
// 到请求顺序；数量不符或标识不匹配 → 描述性错误（fail loud）。
func alignScoresByID(pairs []types.PairScoringInput, scores []types.DirectionalPairScore) ([]types.DirectionalPairScore, error) {
	if len(scores) != len(pairs) {
		return nil, fmt.Errorf("bamlllm: ScorePairs 返回 %d 条结果，请求 %d 对（数量不匹配，拒绝按位置对齐）",
			len(scores), len(pairs))
	}
	byID := make(map[[2]string]types.DirectionalPairScore, len(scores))
	for _, s := range scores {
		key := [2]string{s.User1, s.User2}
		if _, dup := byID[key]; dup {
			return nil, fmt.Errorf("bamlllm: ScorePairs 返回重复标识 (%s, %s)", s.User1, s.User2)
		}
		byID[key] = s
	}
	out := make([]types.DirectionalPairScore, 0, len(pairs))
	for _, p := range pairs {
		s, ok := byID[[2]string{p.User1, p.User2}]
		if !ok {
			return nil, fmt.Errorf("bamlllm: ScorePairs 结果缺少请求对 (%s, %s) 的标识回显", p.User1, p.User2)
		}
		out = append(out, s)
	}
	return out, nil
}

// scoreJSON 是 engine 打分解析器期望的响应形状（a_to_b/b_to_a/reasoning）。
type scoreJSON struct {
	AToB      float64 `json:"a_to_b"`
	BToA      float64 `json:"b_to_a"`
	Reasoning string  `json:"reasoning"`
}

// routeExtract 提取路径：还原 raw_text → ExtractProfile →
// {"skills":...,"vision":...,"project":...,"needs":...}。
func (c *Client) routeExtract(prompt string) (string, error) {
	raw, err := parseExtractPrompt(prompt)
	if err != nil {
		return "", err
	}
	sections, err := baml.ExtractProfile(context.Background(), raw)
	if err != nil {
		return "", fmt.Errorf("baml ExtractProfile: %w", err)
	}
	out, err := json.Marshal(map[string]string{
		"skills":  sections.Skills,
		"vision":  sections.Vision,
		"project": sections.Project,
		"needs":   sections.Needs,
	})
	return string(out), err
}

// routeHyde HyDE 路径：还原分节输入 → GenerateHypothetical →
// JSON 字符串数组（engine.parseDescriptors 的首选解析形状）。
func (c *Client) routeHyde(prompt string) (string, error) {
	name, content, n, err := parseHydePrompt(prompt)
	if err != nil {
		return "", err
	}
	descriptors, err := baml.GenerateHypothetical(context.Background(), name, content, int64(n))
	if err != nil {
		return "", fmt.Errorf("baml GenerateHypothetical: %w", err)
	}
	out, err := json.Marshal(descriptors)
	return string(out), err
}

// routeIntroduce 话术路径：还原双方画像 → DraftIntroduction →
// {"intro":...,"starter_topics":...}（engine.parseIntroResponse 形状）。
func (c *Client) routeIntroduce(prompt string) (string, error) {
	u1, s1, u2, s2, instruction, err := parseIntroPrompt(prompt)
	if err != nil {
		return "", err
	}
	draft, err := baml.DraftIntroduction(context.Background(), u1, s1, u2, s2, instruction)
	if err != nil {
		return "", fmt.Errorf("baml DraftIntroduction: %w", err)
	}
	out, err := json.Marshal(map[string]string{
		"intro":          draft.Intro,
		"starter_topics": draft.Starter_topics,
	})
	return string(out), err
}

// ---------------------------------------------------------------------------
// prompt 还原：从 engine 发出的确定性 prompt 结构中提取类型化输入。
// 标记来自 config/templates.go 的默认模板（golden 门禁约束，变更 =
// 需同步更新本解析器与 golden/baml 快照）。
// ---------------------------------------------------------------------------

// pairHeaderRE 匹配 engine.buildScoringPrompt 的批量对标记：
// "### Pair 3: (alice, bob)"。
var pairHeaderRE = regexp.MustCompile(`(?m)^### Pair \d+: \(([^,\s]+), ([^)\s]+)\)$`)

// parseScorePrompt 从打分 prompt 还原 PairScoringInput 列表与 instruction。
//
// 批量 prompt 形如：
//
//	[批量头（JSON 数组要求）]
//
//	### Pair 1: (alice, bob)
//	<渲染后的 scoring 模板：Person A (user1): … / Person B (user2): … / Instruction: …>
//
//	### Pair 2: (carol, david)
//	…
func parseScorePrompt(prompt string) ([]types.PairScoringInput, string, error) {
	locs := pairHeaderRE.FindAllStringSubmatchIndex(prompt, -1)
	if len(locs) == 0 {
		return nil, "", fmt.Errorf("bamlllm: 打分 prompt 缺少 \"### Pair N: (u1, u2)\" 标记（自定义模板请保留默认标记结构）")
	}
	instruction := ""
	pairs := make([]types.PairScoringInput, 0, len(locs))
	for i, loc := range locs {
		u1, u2 := prompt[loc[2]:loc[3]], prompt[loc[4]:loc[5]]
		blockStart := loc[1]
		blockEnd := len(prompt)
		if i+1 < len(locs) {
			blockEnd = locs[i+1][0]
		}
		s1, s2, instr, err := parseScoringBlock(prompt[blockStart:blockEnd])
		if err != nil {
			return nil, "", fmt.Errorf("pair (%s, %s): %w", u1, u2, err)
		}
		if instr != "" {
			instruction = instr
		}
		pairs = append(pairs, types.PairScoringInput{
			User1: u1, User2: u2, User1_sections: s1, User2_sections: s2,
		})
	}
	return pairs, instruction, nil
}

// parseScoringBlock 解析单对渲染块中的双方 sections 与 instruction
// （默认模板标记：Person A (user1): / Person B (user2): / Instruction:）。
//
// instruction 截至其后首个空行（recipe.instruction 是折叠标量，
// 内部无空行；模板在 {instruction} 后固定接空行 + "Score from..."）。
func parseScoringBlock(block string) (s1, s2, instruction string, err error) {
	const (
		markerA = "Person A (user1):"
		markerB = "Person B (user2):"
		markerI = "Instruction:"
	)
	iA := strings.Index(block, markerA)
	iB := strings.Index(block, markerB)
	iI := strings.Index(block, markerI)
	if iA == -1 || iB == -1 || iI == -1 || !(iA < iB && iB < iI) {
		return "", "", "", fmt.Errorf("打分块缺少 Person A/B 与 Instruction 标记（默认模板结构）")
	}
	s1 = strings.TrimSpace(block[iA+len(markerA) : iB])
	s2 = strings.TrimSpace(block[iB+len(markerB) : iI])
	instruction = firstParagraph(block[iI+len(markerI):])
	return s1, s2, instruction, nil
}

// firstParagraph 返回 s 首个空行之前的内容（TrimSpace）。
func firstParagraph(s string) string {
	if i := strings.Index(s, "\n\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// parseExtractPrompt 从提取 prompt 还原 raw_text
// （默认模板标记：Profile text: … Extract into these sections）。
//
// 边界（qodo PR2 #2）：画像文本插值在两个标记**之间**，且可能包含
// 标记字样本身。begin 用首个出现（模板头在 raw_text 之前，首个必是
// 模板的）；end 用**末个**出现（模板指令在 raw_text 之后，末个必是
// 模板的）——用首个会把含该短语的画像静默截断。
func parseExtractPrompt(prompt string) (string, error) {
	const (
		begin = "Profile text:"
		end   = "Extract into these sections"
	)
	iB := strings.Index(prompt, begin)
	iE := strings.LastIndex(prompt, end)
	if iB == -1 || iE == -1 || iE <= iB {
		return "", fmt.Errorf("bamlllm: 提取 prompt 缺少 Profile text / Extract into these sections 标记")
	}
	return strings.TrimSpace(prompt[iB+len(begin) : iE]), nil
}

// hydeSectionRE 匹配 HyDE prompt 的 "Section: name" / "Content: …" 行。
var (
	hydeSectionRE = regexp.MustCompile(`(?m)^Section:\s*(.+)$`)
	hydeContentRE = regexp.MustCompile(`(?s)^Content:\s*(.+?)\n\s*\n`)
	hydeCountRE   = regexp.MustCompile(`Write\s+(\d+)\s+hypothetical`)
)

// parseHydePrompt 从 HyDE prompt 还原分节名、内容与描述符数量。
//
// 内容截至 "Write N hypothetical" 计数行：取**末个**正则匹配——
// 分节内容自身可能含 "Write …" 字样，首个匹配会把内容截断
// （与 parseExtractPrompt 的末界原则同源，qodo PR2 #2）。
func parseHydePrompt(prompt string) (name, content string, n int, err error) {
	sec := hydeSectionRE.FindStringSubmatch(prompt)
	if sec == nil {
		return "", "", 0, fmt.Errorf("bamlllm: HyDE prompt 缺少 \"Section:\" 行")
	}
	name = strings.TrimSpace(sec[1])
	iC := strings.Index(prompt, "Content:")
	if iC == -1 {
		return "", "", 0, fmt.Errorf("bamlllm: HyDE prompt 缺少 \"Content:\" 行")
	}
	rest := prompt[iC+len("Content:"):]
	n = 1
	if locs := hydeCountRE.FindAllStringSubmatchIndex(rest, -1); len(locs) > 0 {
		last := locs[len(locs)-1]
		if v, e := strconv.Atoi(rest[last[2]:last[3]]); e == nil {
			n = v
		}
		rest = rest[:last[0]]
	}
	content = strings.TrimSpace(rest)
	return name, content, n, nil
}

// parseIntroPrompt 从话术 prompt 还原双方姓名、sections 与 instruction
// （默认模板标记：Person A: name / Person B: name / Instruction:）。
func parseIntroPrompt(prompt string) (u1, s1, u2, s2, instruction string, err error) {
	lines := strings.Split(prompt, "\n")
	type person struct {
		name  string
		start int
	}
	var people []person
	instrIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Person A:"):
			people = append(people, person{strings.TrimSpace(strings.TrimPrefix(trimmed, "Person A:")), i + 1})
		case strings.HasPrefix(trimmed, "Person B:"):
			people = append(people, person{strings.TrimSpace(strings.TrimPrefix(trimmed, "Person B:")), i + 1})
		case strings.HasPrefix(trimmed, "Instruction:"):
			instrIdx = i
		}
	}
	if len(people) != 2 {
		return "", "", "", "", "", fmt.Errorf("bamlllm: 话术 prompt 缺少 Person A:/Person B: 标记")
	}
	sectionEnd := len(lines)
	if instrIdx >= 0 {
		sectionEnd = instrIdx
		instruction = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[instrIdx]), "Instruction:"))
	}
	// sections 块：Person 行之后到首个空行（FormatSections 输出不含
	// 空行；模板在 {user*_sections} 后固定接空行 + 下一段说明）。
	block := func(start int) string {
		end := sectionEnd
		for i := start; i < sectionEnd; i++ {
			if strings.TrimSpace(lines[i]) == "" {
				end = i
				break
			}
		}
		return strings.TrimSpace(strings.Join(lines[start:end], "\n"))
	}
	s1 = block(people[0].start)
	s2 = block(people[1].start)
	return people[0].name, s1, people[1].name, s2, instruction, nil
}
