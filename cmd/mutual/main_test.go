package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runCLI 执行 CLI 主入口并捕获输出（evaluate/calibrate 全离线，无需凭据）。
func runCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	oldArgs, oldStdout, oldStderr := os.Args, os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Args = append([]string{"mutual"}, args...)
	os.Stdout, os.Stderr = wOut, wErr

	done := make(chan struct{})
	var outBuf, errBuf bytes.Buffer
	go func() {
		_, _ = outBuf.ReadFrom(rOut)
		_, _ = errBuf.ReadFrom(rErr)
		close(done)
	}()

	// main() 会 os.Exit；改为直接分发子命令。
	code := dispatch(args[0], args[1:])
	_ = wOut.Close()
	_ = wErr.Close()
	<-done
	os.Args, os.Stdout, os.Stderr = oldArgs, oldStdout, oldStderr
	return code, outBuf.String() + errBuf.String()
}

// dispatch 与 main 的 switch 同构（测试不触发 os.Exit）。
func dispatch(cmd string, args []string) int {
	switch cmd {
	case "evaluate":
		return cmdEvaluate(args)
	case "calibrate":
		return cmdCalibrate(args)
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知命令 %q\n\n", cmd)
		usage()
		return 2
	}
}

// TestCLIEvaluateJSON --json 输出可解析，顶层是门禁报告
// （envy_count_left = 三场景 + market 总和，right = 0），门禁通过。
func TestCLIEvaluateJSON(t *testing.T) {
	code, out := runCLI(t, "evaluate", "--json")
	if code != 0 {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("JSON 输出解析失败: %v\n%s", err, out)
	}
	perBench, ok := payload["per_bench"].(map[string]any)
	if !ok || len(perBench) != 4 {
		t.Fatalf("per_bench 应含 4 项: %v", payload["per_bench"])
	}
	for _, name := range []string{"classic", "drift", "cold", "market"} {
		if _, ok := perBench[name]; !ok {
			t.Errorf("per_bench 缺 %s", name)
		}
	}
	if payload["hr_at_3"].(float64) < 0.6 {
		t.Errorf("门禁 hr_at_3=%.3f 低于 0.6", payload["hr_at_3"])
	}
	if left := payload["envy_count_left"].(float64); left > 2 {
		t.Errorf("门禁 total_envy=%.0f 超过 2", left)
	}
}

// TestCLIEvaluateGateExitCode --fail-on-gate：门禁未达标非零退出
// （用极严苛的自定义门禁构造 FAIL 分支）。
func TestCLIEvaluateGateExitCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "strict.yaml")
	yaml := "evaluation:\n  gates:\n    hr_at_3_min: 1.1\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatalf("写入严格门禁: %v", err)
	}
	code, out := runCLI(t, "evaluate", "--config", path, "--fail-on-gate", "--json")
	if code != 1 {
		t.Errorf("严格门禁应退出 1: got %d\n%s", code, out)
	}
}

// TestCLIEvaluateText 非 JSON 输出含 PASS 判定与门禁数值。
func TestCLIEvaluateText(t *testing.T) {
	code, out := runCLI(t, "evaluate")
	if code != 0 {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	for _, frag := range []string{"classic", "drift", "cold", "market", "PASS"} {
		if !strings.Contains(out, frag) {
			t.Errorf("输出缺 %q:\n%s", frag, out)
		}
	}
}

// TestCLICalibrate 评测历史驱动的校准输出（prompt 块 + 权重调整）。
func TestCLICalibrate(t *testing.T) {
	history := `[
  {"hr_at_1": 0.9, "hr_at_3": 0.9, "hr_at_5": 0.9, "ndcg_at_5": 0.8,
   "envy_count_left": 0, "envy_count_right": 0, "total_scenarios": 24},
  {"hr_at_1": 0.7, "hr_at_3": 0.6, "hr_at_5": 0.6, "ndcg_at_5": 0.5,
   "envy_count_left": 1, "envy_count_right": 0, "total_scenarios": 24}
]`
	path := filepath.Join(t.TempDir(), "history.json")
	if err := os.WriteFile(path, []byte(history), 0o644); err != nil {
		t.Fatalf("写入历史: %v", err)
	}
	code, out := runCLI(t, "calibrate", "--history", path)
	if code != 0 {
		t.Fatalf("exit=%d out=%s", code, out)
	}
	if !strings.Contains(out, "Prompt 校准块") || !strings.Contains(out, "权重校准") {
		t.Errorf("校准输出缺段:\n%s", out)
	}
	// HR 下降（0.9→0.6）应触发权重步进。
	if !strings.Contains(out, "embed_weight:0.30 llm_weight:0.70") {
		t.Errorf("HR 下降应触发 llm+step:\n%s", out)
	}
}

// TestCLICalibrateMissingHistory --history 缺失 → 非零退出。
func TestCLICalibrateMissingHistory(t *testing.T) {
	code, _ := runCLI(t, "calibrate")
	if code != 2 {
		t.Errorf("缺 --history 应退出 2: got %d", code)
	}
}

// TestCLIUnknownCommand 未知命令 → 退出 2 + usage。
func TestCLIUnknownCommand(t *testing.T) {
	code, out := runCLI(t, "frobnicate")
	if code != 2 || !strings.Contains(out, "未知命令") {
		t.Errorf("未知命令应退出 2: got %d\n%s", code, out)
	}
}

// TestCLIEvaluateExtended --extended 诊断旗标：扩展套件全场景输出、
// 资格过滤元数据可见、不影响门禁退出码。
func TestCLIEvaluateExtended(t *testing.T) {
	code, out := runCLI(t, "evaluate", "--config", filepath.Join("..", "..", "config", "default.yaml"), "--extended")
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	for _, want := range []string{"扩展陷阱套件", "paraphrase", "decoy", "messy", "constraints", "zh_assoc", "资格排除"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺 %q:\n%s", want, out)
		}
	}
	// 门禁结果仍在前段（--extended 不改变门禁判定）
	if !strings.Contains(out, "PASS") && !strings.Contains(out, "FAIL") {
		t.Fatalf("缺门禁判定:\n%s", out)
	}
}
