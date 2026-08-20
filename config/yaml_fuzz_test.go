package config

import (
	"reflect"
	"testing"
)

// FuzzParseYAML（mutual #5，Scorecard Fuzzing=0）：任意输入不 panic；
// 成功解析者必须（a）确定性——同输入二次解析结果深度相等；
//（b）可再解析——默认配置的子集语法不含锚点/引用，成功输入自身再喂
// 仍应成功（注释与缩进保持不变时）。种子覆盖 default.yaml 的典型形态。
func FuzzParseYAML(f *testing.F) {
	f.Add([]byte("a: 1\nb: true\nc: null\n"))
	f.Add([]byte("root:\n  child: [1, 3, 5]\n  other: |\n    line1\n    line2\n"))
	f.Add([]byte("folded: >\n  one\n  two\nscalar: 3.25\n"))
	f.Add([]byte("# only comment\n"))
	f.Add([]byte("nested:\n  deeper:\n    deepest: \"quoted # not comment\"\n"))
	f.Add([]byte("empty_map: {}\ninline_list: []\n"))
	f.Add([]byte(":\n"))       // 空键：修复前静默产出 map[""]（fuzz 发现）
	f.Add([]byte("a: 1\n:\n")) // 尾随空键
	f.Fuzz(func(t *testing.T, data []byte) {
		m1, err := ParseYAML(data)
		if err != nil {
			return
		}
		m2, err2 := ParseYAML(data)
		if err2 != nil {
			t.Fatalf("同输入首次成功、二次失败（不确定性）: %v", err2)
		}
		if !reflect.DeepEqual(m1, m2) {
			t.Fatalf("同输入两次解析结果不一致")
		}
		// 成功解析的输出必须只含契约类型（递归：值可为嵌套 mapping）
		var checkVal func(v any) bool
		checkVal = func(v any) bool {
			switch x := v.(type) {
			case nil, bool, int, float64, string:
				return true
			case []any:
				for _, e := range x {
					if !checkVal(e) {
						return false
					}
				}
				return true
			case map[string]any:
				for _, e := range x {
					if !checkVal(e) {
						return false
					}
				}
				return true
			default:
				return false
			}
		}
		for k, v := range m1 {
			if k == "" {
				t.Fatalf("产出空键: %v", m1)
			}
			if !checkVal(v) {
				t.Fatalf("值类型越界 %T（契约：nil/bool/int/float/string/[]any/嵌套map）", v)
			}
		}
	})
}
