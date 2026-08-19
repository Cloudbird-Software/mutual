package engine

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// GenerateHyde 为每个分节生成假设性描述（hyde 阶段，纯变换）。
//
// Hypothetical Document Embeddings：如 "需要技术合作" → "寻找会 Go 的
// 开发者"，增强 embedding 语义召回。填充为 NotSpecified 的分节不生成
// 描述符（缺失 = 中性，其 HyDE 向量在 embed 层为零向量）。
//
// 失败语义（spec 沉默 A-11）：hyde 无 failed_out 契约——单个分节的
// LLM 调用失败时该分节得到空描述符列表，pipeline 继续，不中断。
//
// 解析顺序（spec 沉默 A-15）：
//  1. JSON 数组（["d1","d2"]）→ 取前 n 条非空字符串；
//  2. 自由文本 → 按行切分，剥离 - / * / • / 1. 等项目符号。
func GenerateHyde(
	sections []domain.ExtractedSections,
	nDescriptors int,
	template string,
	model string,
	llm LLMClient,
) map[domain.UserID]domain.HydeDescriptors {
	if nDescriptors < 1 {
		nDescriptors = 1
	}
	result := make(map[domain.UserID]domain.HydeDescriptors, len(sections))
	for _, es := range sections {
		descriptors := map[domain.SectionName][]string{}
		for _, name := range sortedSectionNames(es.Sections) {
			content := es.Sections[name]
			if content == "" || content == NotSpecified {
				continue
			}
			prompt := pyFormatMap(template, map[string]string{
				"section_name":    string(name),
				"section_content": content,
				"n_descriptors":   itoa(nDescriptors),
			})
			response, err := llm.Complete(prompt, model)
			if err != nil {
				continue
			}
			descriptors[name] = parseDescriptors(response, nDescriptors)
		}
		result[es.ID] = domain.HydeDescriptors{ID: es.ID, Descriptors: descriptors}
	}
	return result
}

// sortedSectionNames 按分节名排序（Go map 无序；Python 按插入序，
// 此处排序保证确定性，见 extract.go 的同类说明）。
func sortedSectionNames(m map[domain.SectionName]string) []domain.SectionName {
	names := make([]domain.SectionName, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool { return names[i] < names[j] })
	return names
}

// parseDescriptors 解析 LLM 响应为最多 n 条描述符（A-15 顺序：
// JSON 数组优先，自由文本按行兜底）。
func parseDescriptors(response string, n int) []string {
	text := strings.TrimSpace(response)
	if text == "" {
		return nil
	}
	var arr []any
	if err := json.Unmarshal([]byte(text), &arr); err == nil {
		var items []string
		for _, item := range arr {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				items = append(items, strings.TrimSpace(s))
			}
		}
		return capN(items, n)
	}

	var lines []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		line = stripBullets(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return capN(lines, n)
}

// stripBullets 剥离行首项目符号：- / * / • 前缀与 "1." / "1)" 编号。
func stripBullets(line string) string {
	for {
		if len(line) > 0 && (line[0] == '-' || line[0] == '*') {
			line = strings.TrimSpace(line[1:])
			continue
		}
		if strings.HasPrefix(line, "•") {
			line = strings.TrimSpace(line[len("•"):])
			continue
		}
		break
	}
	// "1." / "1)" 形式的编号（数字后跟 . 或 )）。
	if len(line) >= 2 && unicode.IsDigit(rune(line[0])) &&
		(line[1] == '.' || line[1] == ')') {
		line = strings.TrimSpace(line[2:])
	}
	return line
}

func capN(items []string, n int) []string {
	if len(items) > n {
		return items[:n]
	}
	return items
}

func itoa(n int) string { return strconv.Itoa(n) }
