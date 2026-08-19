// Package goldentest 存放跨包的 golden 门禁测试：prompt 契约快照。
//
// BAML-1（generators.baml 注释）：baml_src/*.baml 是 LLM 行为的唯一
// 事实来源；任何 prompt/契约变更都必须同步更新 golden/baml/ 快照，
// 否则门禁阻断——防止“顺手调 prompt”绕过评审。
//
// 快照语义：golden/baml/<name>.baml 是当前已评审通过版本的逐字节
// 副本。变更流程：改 baml_src → npx @boundaryml/baml generate →
// cp baml_src/*.baml golden/baml/ → PR 中三者一起评审。
package goldentest

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBAMLSnapshot baml_src 与 golden/baml 逐字节一致。
func TestBAMLSnapshot(t *testing.T) {
	srcDir := filepath.Join("..", "..", "baml_src")
	goldenDir := filepath.Join("..", "..", "golden", "baml")

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("读取 baml_src: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".baml" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(srcDir, name))
			if err != nil {
				t.Fatalf("读取 baml_src/%s: %v", name, err)
			}
			goldenPath := filepath.Join(goldenDir, name)
			golden, err := os.ReadFile(goldenPath)
			if os.IsNotExist(err) {
				t.Fatalf("golden/baml/%s 不存在：新增契约须同时提交快照（cp baml_src/%s golden/baml/）", name, name)
			}
			if err != nil {
				t.Fatalf("读取 golden/baml/%s: %v", name, err)
			}
			if string(src) != string(golden) {
				t.Errorf("%s 与 golden 快照不一致：prompt/契约变更必须同步更新 golden/baml/ 并在 PR 中说明", name)
			}
		})
	}

	// 反向检查：golden 中无孤儿快照（baml_src 已删除但 golden 残留）。
	goldenEntries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatalf("读取 golden/baml: %v", err)
	}
	for _, e := range goldenEntries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".baml" {
			continue
		}
		if _, err := os.Stat(filepath.Join(srcDir, name)); os.IsNotExist(err) {
			t.Errorf("golden/baml/%s 是孤儿快照：baml_src 中已无对应文件", name)
		}
	}
}
