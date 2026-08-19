// archlint 是 Go 侧依赖边界检查（对应 Python 基线 scripts/arch_check.py，
// 规则来自 spec/02-stages.md 的分层：核心纯变换 / 适配层负责 IO 与编排）。
//
// 分层（低层不得依赖高层）：
//
//	L0 基础层  domain / num / rng            —— 零内部依赖（rng→num 除外，同层）
//	L1 核心层  engine / signal               —— 纯算法，只依赖 L0
//	L2 适配层  config / store / feedback /
//	           bench / pipeline / bamlllm    —— 编排与 IO，依赖 L0/L1
//	L3 入口层  cmd/*                         —— 可依赖一切
//
// baml_client 是生成代码（叶子，不依赖内部包）。
//
// 违规 exit 1（CI `make arch` 门禁的一部分）。
package main

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const modulePath = "github.com/Cloudbird-Software/mutual"

// layerOf 返回内部包的层级；外部依赖与 baml_client 视作 -1（不限）。
var layerOf = map[string]int{
	"internal/domain": 0, "internal/num": 0, "internal/rng": 0,
	"internal/engine": 1, "internal/signal": 1,
	"config": 0 + 2, "internal/store": 2, "internal/feedback": 2,
	"internal/bench": 2, "internal/pipeline": 2, "internal/bamlllm": 2,
	// L3：cmd 下任意子包。
}

// isEntry 判定是否入口层（cmd/* 或根命令包）。
func isEntry(pkg string) bool {
	return pkg == "cmd/mutual" || pkg == "cmd/archlint" || strings.HasPrefix(pkg, "cmd/")
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "::error::%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	violations := []string{}
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == ".venv" ||
				name == "src" || name == "tests" || name == "data" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		pkgDir := filepath.ToSlash(filepath.Dir(rel))
		imports, err := importsOf(path)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		checked++
		fromLayer, _ := layer(pkgDir)
		// -2 = internal/config 下未登记进 layerOf 的包：fail-closed 报违规。
		// 若按 "< 0 外部依赖" 吞掉，新增 internal/xxx 会完全绕过检查，
		// 门禁对新包静默失效（CodeRabbit）。
		if fromLayer == -2 {
			violations = append(violations,
				fmt.Sprintf("%s 未登记进 cmd/archlint/main.go 的 layerOf（新包必须登记，fail-closed）", pkgDir))
		}
		for _, imp := range imports {
			impLayer, impName := layer(imp)
			if impLayer == -2 {
				violations = append(violations,
					fmt.Sprintf("%s -> %s：被导入包未登记进 layerOf（新包必须登记，fail-closed）", pkgDir, impName))
				continue
			}
			if impLayer < 0 || fromLayer < 0 {
				continue // 外部依赖 / 未分层路径（含 baml_client）
			}
			if fromLayer < impLayer {
				violations = append(violations,
					fmt.Sprintf("%s (L%d) -> %s (L%d)", pkgDir, fromLayer, impName, impLayer))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(violations)
	// 去重：未登记包按文件逐个报，同一包会重复出现（CodeRabbit 建议）。
	deduped := violations[:0]
	for i, v := range violations {
		if i == 0 || v != violations[i-1] {
			deduped = append(deduped, v)
		}
	}
	violations = deduped
	if len(violations) > 0 {
		return fmt.Errorf("依赖边界违规（低层依赖高层）：%d 处\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
	fmt.Printf("OK 依赖边界（%d 个 Go 文件，分层单向依赖）\n", checked)
	return nil
}

// layer 返回 (层级, 展示名)。入口层视作 L3；未知内部路径报 -2
// （新包必须登记进 layerOf，防止漏检）。
func layer(pkgDir string) (int, string) {
	if strings.HasPrefix(pkgDir, "cmd/") {
		return 3, pkgDir
	}
	if v, ok := layerOf[pkgDir]; ok {
		return v, pkgDir
	}
	if strings.HasPrefix(pkgDir, "baml_client") {
		return -1, pkgDir
	}
	if strings.HasPrefix(pkgDir, "internal/") || pkgDir == "config" {
		return -2, pkgDir
	}
	return -1, pkgDir // 外部依赖
}

// importsOf 解析单个 Go 文件的全部 import 说明符。
func importsOf(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(file.Imports))
	for _, imp := range file.Imports {
		value := strings.Trim(imp.Path.Value, `"`)
		if strings.HasPrefix(value, modulePath+"/") {
			out = append(out, strings.TrimPrefix(value, modulePath+"/"))
		}
	}
	return out, nil
}
