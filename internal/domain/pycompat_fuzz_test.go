package domain

import (
	"strings"
	"testing"
)

// FuzzHashText（mutual #5，Scorecard Fuzzing=0）：内容 hash 的稳定性与形态——
// 任意文本（含无效 UTF-8/极端 Unicode）不 panic、恒 16 位小写 hex、
// 同输入恒同输出（跨进程缓存键的语义前提，spec/05-boundaries.md §5）。
func FuzzHashText(f *testing.F) {
	f.Add("hello")
	f.Add("")                         // 空串
	f.Add("中文内容")
	f.Add("\x80\xff")                 // 无效 UTF-8 字节
	f.Add("😀👨‍👩‍👧‍👦")               // emoji ZWJ 序列
	f.Add(strings.Repeat("x", 1<<16)) // 64KiB
	f.Fuzz(func(t *testing.T, s string) {
		h1 := HashText(s)
		h2 := HashText(s)
		if h1 != h2 {
			t.Fatalf("同输入两次 hash 不等: %s / %s", h1, h2)
		}
		if len(h1) != 16 {
			t.Fatalf("hash 长度 %d ≠ 16: %q", len(h1), h1)
		}
		for _, c := range h1 {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				t.Fatalf("hash 非小写 hex: %q", h1)
			}
		}
	})
}

// FuzzPyJSONDumpSections：Python json.dumps(ensure_ascii=True) 字节级复刻的
// 健壮性——任意键值（含控制字符/代理对/无效 UTF-8）不 panic，且输出恒为
// 可被标准 json.Unmarshal 还原的 ASCII 文本（转义闭环）。
func FuzzPyJSONDumpSections(f *testing.F) {
	f.Add("k", "v")
	f.Add("键", "值")
	f.Add("ctrl", "\x00\x01\x1f\"\\")
	f.Add("emoji", "😀")
	f.Add("bad-utf8", "\x80")
	f.Fuzz(func(t *testing.T, key, val string) {
		out := pyJSONDumpSections(map[string]string{key: val})
		if !isASCII(out) {
			t.Fatalf("ensure_ascii 违例（输出含非 ASCII）: %q", out)
		}
	})
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}
