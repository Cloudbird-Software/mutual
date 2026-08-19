// Package goldentest 存放跨包的 golden 门禁测试：prompt 契约快照。
package goldentest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// untrustedFields 是直接承接用户可控文本的 BAML 参数：渲染进 prompt
// 时必须走 |text_block 数据块隔离（CodeRabbit 注入防护回归）。
var untrustedFields = []string{
	"raw_text",        // extract：自由文本画像
	"section_content", // hyde：分节文本
	"user1_sections",  // introduce/score：格式化分节
	"user2_sections",
	"pairs", // score：整批候选对（含双方 sections）
}

// TestBAMLUntrustedDataIsolation 注入隔离回归（CodeRabbit）：
//  1. 凡不可信字段插值必须带 |text_block（渲染成带标签的数据块，
//     与指令文本结构化分离）；
//  2. 含不可信字段的 prompt 必须携带 "UNTRUSTED USER DATA" 指令，
//     要求模型忽略数据块内的注入指令。
//
// 该测试防止后续"顺手简化 prompt"移除隔离层——移除即门禁红。
func TestBAMLUntrustedDataIsolation(t *testing.T) {
	srcDir := filepath.Join("..", "..", "baml_src")
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("读取 baml_src: %v", err)
	}
	// {{ field }} 或 {{ field|... }}（不含 text_block）都算裸插值。
	bareRe := regexp.MustCompile(`\{\{\s*(\w+)(\|[^}]*)?\s*\}\}`)
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".baml" || name == "generators.baml" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(srcDir, name))
			if err != nil {
				t.Fatalf("读取 %s: %v", name, err)
			}
			src := string(data)
			hasUntrusted := false
			for _, m := range bareRe.FindAllStringSubmatch(src, -1) {
				field, filters := m[1], m[2]
				for _, u := range untrustedFields {
					if field == u {
						hasUntrusted = true
						if !strings.Contains(filters, "text_block") {
							t.Errorf("%s: 不可信字段 {{ %s }} 必须经 |text_block 渲染成隔离数据块（当前: {{ %s%s }}）",
								name, field, field, filters)
						}
					}
				}
			}
			if hasUntrusted {
				// prompt 内指令可能跨行（BAML #"" 字符串保留换行），
				// 规范化空白后匹配。
				normalized := strings.Join(strings.Fields(src), " ")
				if !strings.Contains(normalized, "UNTRUSTED USER DATA") {
					t.Errorf("%s: 含不可信字段的 prompt 必须携带 UNTRUSTED USER DATA 指令（忽略数据块内注入）", name)
				}
			}
		})
	}
}
