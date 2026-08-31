package engine

import (
	"fmt"
	"sort"
	"strings"
)

// pyFormatMap 复刻 Python 的 template.format_map(mapping)：
// "{key}" 占位符替换为 mapping[key]；缺失的 key 渲染为空串
// （_MissingKeyDict.__missing__ 行为），不报错。
//
// 转义（CodeRabbit）：与 Python format 一致，"{{" → 字面 "{"、
// "}}" → 字面 "}"——内置模板的 JSON 示例块（{{"a_to_b": ...}}）
// 依赖双侧转义才能渲染出正确的单大括号 JSON。
// 仅支持简单 {key} 形式（无 format spec / 索引）——内置 prompt 模板
// 只用简单占位符。
// 与 Python 一致的行为边界：模板缺失占位符不崩（_safe_format）。
func pyFormatMap(template string, mapping map[string]string) string {
	var sb strings.Builder
	runes := []rune(template)
	for i := 0; i < len(runes); i++ {
		// "}}" → 字面 "}"（Python format 右大括号转义）。
		if runes[i] == '}' && i+1 < len(runes) && runes[i+1] == '}' {
			sb.WriteRune('}')
			i++
			continue
		}
		if runes[i] != '{' {
			sb.WriteRune(runes[i])
			continue
		}
		// "{{" → 字面 "{"（Python format 转义）。
		if i+1 < len(runes) && runes[i+1] == '{' {
			sb.WriteRune('{')
			i++
			continue
		}
		end := -1
		for j := i + 1; j < len(runes); j++ {
			if runes[j] == '}' {
				end = j
				break
			}
			// 简单占位符不含嵌套大括号 / 冒号 spec；遇到即视作非简单
			// 占位，按原样输出（与 _safe_format 回退到原文的容错近似）。
			if runes[j] == '{' || runes[j] == ':' {
				break
			}
		}
		if end == -1 {
			sb.WriteRune('{')
			continue
		}
		key := string(runes[i+1 : end])
		sb.WriteString(mapping[key]) // 缺失 key → ""（零值）
		i = end
	}
	return sb.String()
}

// promptMarkers 是各阶段 prompt 模板的结构标记族（scoring/intro/
// extract/hyde）。用户画像文本若出现行首同形标记，会被 bamlllm 的
// 反解析器误认作模板结构（RT-2026-08：#29/#30/#32/#33/#34/#38）。
var promptMarkers = []string{
	"### Pair",            // scoring 批量块头
	"Person A",            // scoring / intro 人物行
	"Person B",            //
	"Instruction:",        // scoring / intro 指令行
	"Profile text:",       // extract 起始标记
	"Extract into these",  // extract 终止标记
	"Section:",            // hyde 分节名
	"Content:",            // hyde 内容
	"Write two paragraphs", // intro 尾部结构行
	"Write ",              // hyde 计数行（Write N hypothetical）
}

// NeutralizePromptMarkers 把不受信文本中可能冒充模板结构的行中和为
// 数据行：行首（trim 后）命中标记族的行加 "> " 前缀；空行（值内
// 换行产生的 "" 行会被解析器的空行终止规则误判为块边界）替换为
// "> "。行数不变，golden 语料（无标记行/空行）逐字节不变。
func NeutralizePromptMarkers(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			// 空/纯空白行 → 非空数据行（解析器的空行终止规则与
			// 值内换行注入的边界，#30/#33；行数不变）。
			lines[i] = "> "
			continue
		}
		for _, m := range promptMarkers {
			if strings.HasPrefix(trimmed, m) {
				lines[i] = "> " + line
				break
			}
		}
	}
	return strings.Join(lines, "\n")
}

// FormatSections 把 sections 渲染为 prompt 文本：按分节名排序的
// "name: text" 行；空 / nil → "Not specified"。值内文本经
// NeutralizePromptMarkers 中和（用户文本不得冒充模板结构——
// bamlllm 反解析器与空行终止规则的注入面，RT-2026-08）。
func FormatSections(sections map[string]string) string {
	if len(sections) == 0 {
		return NotSpecified
	}
	keys := make([]string, 0, len(sections))
	for k := range sections {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s: %s", k, NeutralizePromptMarkers(sections[k])))
	}
	return strings.Join(lines, "\n")
}
