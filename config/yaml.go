// Package config 是 mutual 的配置层：YAML 加载、目录/文件 overlay、
// 单值 override 与 prompt 模板解析。
//
// 定位（spec 的参数层）：所有可调参数集中在 config/default.yaml，
// 实现代码不硬编码参数。本包与 Python 基线 src/mutual/config.py 的
// load_config / resolve_prompt_templates 语义对齐。
//
// YAML 支持范围：本仓库配置文件实际使用的子集——嵌套 mapping
// （2 空格缩进）、标量（null/bool/int/float/字符串）、行内列表
// （[1, 3, 5]）、折叠块标量（>）与字面块标量（|）、注释。刻意不做
// 完整 YAML 实现：配置格式由本仓库控制，子集解析器可被完整测试
// 覆盖（config_test.go 对 default.yaml 全字段断言）。
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseYAML 解析配置子集 YAML → map[string]any。
//
// 值类型：nil（null/~）、bool、int、float64、string、[]any（行内列表）。
func ParseYAML(data []byte) (map[string]any, error) {
	lines, err := scanLines(string(data))
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return map[string]any{}, nil
	}
	val, next, err := parseBlock(lines, 0, lines[0].indent)
	if err != nil {
		return nil, err
	}
	if next != len(lines) {
		return nil, fmt.Errorf("yaml: 第 %d 行缩进异常（多余内容）", lines[next].num)
	}
	m, ok := val.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("yaml: 顶层必须是 mapping")
	}
	return m, nil
}

// rawLine 是预处理后的有效行（去注释、去尾空白）。
type rawLine struct {
	indent int
	text   string
	num    int
}

// scanLines 逐行扫描：跳过空行/注释行，剥离行内注释，记录缩进。
func scanLines(src string) ([]rawLine, error) {
	var out []rawLine
	for i, raw := range strings.Split(src, "\n") {
		// tab 缩进在 YAML 中非法，尽早报错。
		if strings.HasPrefix(raw, "\t") {
			return nil, fmt.Errorf("yaml: 第 %d 行使用 tab 缩进（仅允许空格）", i+1)
		}
		trimmed := strings.TrimRight(stripComment(raw), " \r")
		if strings.TrimSpace(trimmed) == "" {
			continue
		}
		indent := 0
		for indent < len(trimmed) && trimmed[indent] == ' ' {
			indent++
		}
		out = append(out, rawLine{indent: indent, text: trimmed[indent:], num: i + 1})
	}
	return out, nil
}

// stripComment 剥离行内注释：# 在行首（整行注释）或 # 前有空白，
// 且不在引号内。
func stripComment(s string) string {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if inSingle || inDouble {
				continue
			}
			if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
				return s[:i]
			}
		}
	}
	return s
}

// parseBlock 解析从 start 开始、缩进为 indent 的 mapping 块，
// 返回 (map, 下一行下标)。
func parseBlock(lines []rawLine, start, indent int) (any, int, error) {
	out := map[string]any{}
	i := start
	for i < len(lines) {
		ln := lines[i]
		if ln.indent < indent {
			break
		}
		if ln.indent > indent {
			return nil, i, fmt.Errorf("yaml: 第 %d 行缩进异常", ln.num)
		}
		key, rest, ok := splitKeyValue(ln.text)
		if !ok {
			return nil, i, fmt.Errorf("yaml: 第 %d 行不是 key: value 形式: %q", ln.num, ln.text)
		}
		key = unquote(key)
		if key == "" {
			// 空键（":\n" / ": v" 形态）是语法错误——fuzz 发现此前静默产出 map[""]
			return nil, i, fmt.Errorf("yaml: 第 %d 行键为空", ln.num)
		}
		if rest == "" {
			// 值为空：嵌套块（更深缩进）或 null。
			if i+1 < len(lines) && lines[i+1].indent > indent {
				child, next, err := parseBlock(lines, i+1, lines[i+1].indent)
				if err != nil {
					return nil, i, err
				}
				out[key] = child
				i = next
				continue
			}
			out[key] = nil
			i++
			continue
		}
		// 块标量：> 折叠 / | 字面（可带 - / + 修饰）。
		if rest == ">" || rest == "|" || rest == ">-" || rest == "|-" || rest == ">+" || rest == "|+" {
			text, next := readBlockScalar(lines, i+1, indent, rest)
			out[key] = text
			i = next
			continue
		}
		val, err := parseScalarOrList(rest)
		if err != nil {
			return nil, i, fmt.Errorf("yaml: 第 %d 行 %w", ln.num, err)
		}
		out[key] = val
		i++
	}
	return out, i, nil
}

// readBlockScalar 读取块标量体（缩进 > keyIndent 的连续行）。
//
// 折叠（>）：段内行以单空格连接，空行分段；字面（|）：保留换行。
// 修饰：- 去尾换行；默认 clip（单个尾换行）。
func readBlockScalar(lines []rawLine, start, keyIndent int, style string) (string, int) {
	var body []string
	minIndent := -1
	i := start
	for i < len(lines) {
		ln := lines[i]
		if ln.indent <= keyIndent {
			break
		}
		if minIndent == -1 || ln.indent < minIndent {
			minIndent = ln.indent
		}
		body = append(body, strings.Repeat(" ", ln.indent)+ln.text)
		i++
	}
	// 去掉统一的最小缩进。
	for k, b := range body {
		if len(b) >= minIndent {
			body[k] = b[minIndent:]
		}
	}
	var text string
	if strings.HasPrefix(style, ">") {
		// 折叠：空行 → 段落分隔（\n），段内行 → 单空格连接。
		var parts []string
		var cur []string
		for _, b := range body {
			if strings.TrimSpace(b) == "" {
				parts = append(parts, strings.Join(cur, " "))
				cur = nil
				continue
			}
			cur = append(cur, b)
		}
		parts = append(parts, strings.Join(cur, " "))
		text = strings.Join(parts, "\n")
	} else {
		text = strings.Join(body, "\n")
	}
	// clip / strip chomping。
	if strings.HasSuffix(style, "-") {
		return text, i
	}
	if text != "" {
		text += "\n"
	}
	return text, i
}

// splitKeyValue 拆出 key 与 ":" 之后的原始值文本。
func splitKeyValue(s string) (string, string, bool) {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ':':
			if inSingle || inDouble {
				continue
			}
			if i+1 == len(s) {
				return s[:i], "", true
			}
			if s[i+1] == ' ' {
				return s[:i], strings.TrimSpace(s[i+1:]), true
			}
		}
	}
	return "", "", false
}

// parseScalarOrList 解析行内值：null / bool / 数字 / 引号串 / 行内列表 / 裸串。
func parseScalarOrList(s string) (any, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	if strings.HasPrefix(s, "[") {
		if !strings.HasSuffix(s, "]") {
			return nil, fmt.Errorf("行内列表未闭合: %q", s)
		}
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			return []any{}, nil
		}
		var items []any
		for _, part := range splitTopLevel(inner) {
			v, err := parseScalarOrList(strings.TrimSpace(part))
			if err != nil {
				return nil, err
			}
			items = append(items, v)
		}
		return items, nil
	}
	if strings.HasPrefix(s, "\"") || strings.HasPrefix(s, "'") {
		return unquote(s), nil
	}
	switch s {
	case "null", "Null", "NULL", "~":
		return nil, nil
	case "true", "True", "TRUE":
		return true, nil
	case "false", "False", "FALSE":
		return false, nil
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, nil
	}
	return s, nil
}

// splitTopLevel 按顶层逗号切分（引号外的逗号）。
func splitTopLevel(s string) []string {
	var parts []string
	inSingle, inDouble := false, false
	last := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case ',':
			if !inSingle && !inDouble {
				parts = append(parts, s[last:i])
				last = i + 1
			}
		}
	}
	parts = append(parts, s[last:])
	return parts
}

// unquote 剥离一层引号（双引号处理转义）。
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if v, err := strconv.Unquote(s); err == nil {
			return v
		}
		return s[1 : len(s)-1]
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	return s
}

// KeyOrder 返回 YAML 源中 path 处 mapping 的键序（文件序）。
//
// 为什么需要：ParseYAML 产出 map[string]any（Go map 不保序），而
// Python dict 按 YAML 插入序迭代——cross_section_weights 这类**顺序
// 敏感**配置（浮点累加顺序影响末位精度，qodo PR2 #6）需单独捕获
// 文件序。path 不存在、值为 null 或非 mapping → nil。
//
// 块标量体（如 recipe.instruction: > 的多行正文）缩进更深，扫描时
// 自然跳过，不会被误认作 mapping 键。
func KeyOrder(data []byte, path ...string) []string {
	lines, err := scanLines(string(data))
	if err != nil {
		return nil
	}
	start, indent := 0, 0
	for _, key := range path {
		found, childIndent := -1, -1
		for i := start; i < len(lines); i++ {
			ln := lines[i]
			if ln.indent < indent {
				break // 离开当前块：目标键不在此层
			}
			if ln.indent > indent {
				continue // 块标量体 / 更深层：跳过
			}
			if k, rest, ok := splitKeyValue(ln.text); ok && unquote(k) == key && rest == "" {
				if i+1 < len(lines) && lines[i+1].indent > indent {
					found, childIndent = i+1, lines[i+1].indent
				}
				break
			}
		}
		if found == -1 {
			return nil
		}
		start, indent = found, childIndent
	}
	var keys []string
	for i := start; i < len(lines); i++ {
		ln := lines[i]
		if ln.indent < indent {
			break
		}
		if ln.indent > indent {
			continue
		}
		if k, _, ok := splitKeyValue(ln.text); ok {
			keys = append(keys, unquote(k))
		}
	}
	return keys
}
