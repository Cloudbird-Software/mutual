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
// 仅支持简单 {key} 形式（无 format spec / 索引）——内置 prompt 模板
// 只用简单占位符；遇到 "{{" / "}}" 字面量大括号按字面输出。
// 与 Python 一致的行为边界：模板缺失占位符不崩（_safe_format）。
func pyFormatMap(template string, mapping map[string]string) string {
	var sb strings.Builder
	runes := []rune(template)
	for i := 0; i < len(runes); i++ {
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

// FormatSections 把 sections 渲染为 prompt 文本：按分节名排序的
// "name: text" 行；空 / nil → "Not specified"。
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
		lines = append(lines, fmt.Sprintf("%s: %s", k, sections[k]))
	}
	return strings.Join(lines, "\n")
}

// FormatRawText 把 profile 原始分节渲染为 extract prompt 的 raw_text
// （Python 侧按 sections dict 的插入序 join；Go 侧配置加载时保序，
// 由调用方传入已保序键列表）。
func FormatRawText(sections []struct{ Name, Text string }) string {
	lines := make([]string, 0, len(sections))
	for _, s := range sections {
		lines = append(lines, fmt.Sprintf("%s: %s", s.Name, s.Text))
	}
	return strings.Join(lines, "\n")
}
