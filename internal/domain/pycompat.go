// Package domain 定义 mutual 管线的强类型数据契约（IO schemas）。
//
// 每个类型对应 spec/01-schemas.md 中的一个契约，与 Python 基线
// （src/mutual/schemas.py）的 dataclass 一一对应。实现代码可以随时
// 重写，但这些类型的字段和语义不可随意修改——它们是管线的
// "可执行 spec"，golden 差分测试以字段级一致作为等价性证据。
//
// 设计原则（AI 阅读友好，见 docs/AI-GUIDE.md）：
//   - 强类型 ID：UserID / SectionName / PairID 是独立 string 类型，
//     编译器阻止把任意字符串误用作 ID；
//   - 构造即规范：构造函数内建不变量（如 pair 顺序归一化），
//     调用方无法构造出违反不变量的值；
//   - 显式可选：Python 的 Optional[float] 对应 Go 的 *float64；
//   - 序列化对齐：ToMap 与 Python to_dict 逐字段一致（含
//     round(x, 3) 的 banker's rounding），见 pycompat.go。
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// HashText 返回内容 hash：sha256(text) 十六进制前 16 位。
//
// 用途：embedding / LLM 缓存的 content-addressed key。
// 禁止用 Go 内置 map hash——每次进程运行不同，缓存无法跨 run 命中
// （对应 Python 基线禁用内置 hash() 的理由，spec/05-boundaries.md §5）。
func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

// PyRound 复刻 Python 的 round(x, n)：十进制正确舍入 + ties-to-even，
// 结果转回 float64。golden 差分测试依赖它与 Python 逐位一致。
//
// 实现说明：FormatFloat('f', n) 对二进制精确值做同样的
// round-half-even 十进制舍入，再 ParseFloat 转回，与
// CPython _Py_dg_dtoa 路径等价。
func PyRound(x float64, n int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	s := strconv.FormatFloat(x, 'f', n, 64)
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return x
	}
	return v
}

// pyJSONDumpSections 复刻 Python json.dumps(sections, sort_keys=True)：
// 分隔符为 (", ", ": ")、ensure_ascii 转义、键排序。
// ExtractedSections.Hash 依赖其字节级一致（内容 hash 的输入）。
func pyJSONDumpSections(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			sb.WriteString(", ")
		}
		pyWriteJSONString(&sb, k)
		sb.WriteString(": ")
		pyWriteJSONString(&sb, m[k])
	}
	sb.WriteByte('}')
	return sb.String()
}

// pyWriteJSONString 复刻 Python json.dumps 的字符串转义
// （ensure_ascii=True：非 ASCII 一律转 \uXXXX，小写十六进制）。
func pyWriteJSONString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		default:
			if r < 0x20 || r >= 0x7f {
				if r > 0xffff {
					// 星面字符：UTF-16 代理对（Python ensure_ascii 行为）。
					hi, lo := utf16.EncodeRune(r)
					writeUEscape(sb, hi)
					writeUEscape(sb, lo)
				} else {
					writeUEscape(sb, r)
				}
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
}

func writeUEscape(sb *strings.Builder, r rune) {
	const hexDigits = "0123456789abcdef"
	sb.WriteString(`\u`)
	sb.WriteByte(hexDigits[(r>>12)&0xf])
	sb.WriteByte(hexDigits[(r>>8)&0xf])
	sb.WriteByte(hexDigits[(r>>4)&0xf])
	sb.WriteByte(hexDigits[r&0xf])
}
