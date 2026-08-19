package bench

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// TestBenchGoldenSuite 逐位对拍 Python 基线捕获的
// golden/evaluation_report.json（capture 参数：seed=0、noise=0.24）。
//
// 差分覆盖两处最容易走样的边界（bench.go 模块注释）：
//   - RNG 流消费顺序（黄金对扰动 → LR 噪声 → RL 噪声；member × pool × 双向）；
//   - tuple 逆序排序语义（weight 降序、pid 降序）。
func TestBenchGoldenSuite(t *testing.T) {
	reports, err := RunSuite(0, 0.24)
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}

	raw, err := os.ReadFile("../../golden/evaluation_report.json")
	if err != nil {
		t.Fatalf("读取 golden evaluation_report.json: %v", err)
	}
	var golden struct {
		HRAt1          float64 `json:"hr_at_1"`
		HRAt3          float64 `json:"hr_at_3"`
		HRAt5          float64 `json:"hr_at_5"`
		NDCGAt5        float64 `json:"ndcg_at_5"`
		EnvyCountLeft  int     `json:"envy_count_left"`
		EnvyCountRight int     `json:"envy_count_right"`
		TotalScenarios int     `json:"total_scenarios"`
		PerBench       map[string]struct {
			HRAt3          float64 `json:"hr_at_3"`
			NDCGAt5        float64 `json:"ndcg_at_5"`
			EnvyCountLeft  int     `json:"envy_count_left"`
			EnvyCountRight int     `json:"envy_count_right"`
			TotalScenarios int     `json:"total_scenarios"`
		} `json:"per_bench"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("解析 golden: %v", err)
	}

	for _, name := range append([]string{"classic", "drift", "cold"}, "market") {
		got, ok := reports[name]
		if !ok {
			t.Fatalf("RunSuite 缺场景 %s", name)
		}
		want := golden.PerBench[name]
		if diff := math.Abs(got.HRAt3 - want.HRAt3); diff > 1e-9 {
			t.Errorf("%s hr_at_3: got %v want %v", name, got.HRAt3, want.HRAt3)
		}
		if diff := math.Abs(got.NDCGAt5 - want.NDCGAt5); diff > 1e-9 {
			t.Errorf("%s ndcg_at_5: got %v want %v", name, got.NDCGAt5, want.NDCGAt5)
		}
		if got.EnvyCountLeft != want.EnvyCountLeft || got.EnvyCountRight != want.EnvyCountRight {
			t.Errorf("%s envy: got (%d,%d) want (%d,%d)", name,
				got.EnvyCountLeft, got.EnvyCountRight, want.EnvyCountLeft, want.EnvyCountRight)
		}
		if got.TotalScenarios != want.TotalScenarios {
			t.Errorf("%s total_scenarios: got %d want %d", name, got.TotalScenarios, want.TotalScenarios)
		}
	}

	// golden 顶层是 CLI 门禁报告（cli.py cmd_evaluate）：HR/NDCG 为
	// 三场景加权聚合（round 4），envy_count_left = 三场景 + market 的
	// envy 总和（门禁只看总和，左右分解无意义）。
	agg := AggregateReports([]domain.EvaluationReport{
		reports["classic"], reports["drift"], reports["cold"],
	}, ScenarioNames)
	if got := domain.PyRound(agg.HRAt3, 4); got != golden.HRAt3 {
		t.Errorf("聚合 hr_at_3: got %v want %v", got, golden.HRAt3)
	}
	if got := domain.PyRound(agg.NDCGAt5, 4); got != golden.NDCGAt5 {
		t.Errorf("聚合 ndcg_at_5: got %v want %v", got, golden.NDCGAt5)
	}
	if agg.TotalScenarios != golden.TotalScenarios {
		t.Errorf("聚合 total_scenarios: got %d want %d", agg.TotalScenarios, golden.TotalScenarios)
	}
	totalEnvy := agg.TotalEnvy() + reports["market"].TotalEnvy()
	if totalEnvy != golden.EnvyCountLeft || golden.EnvyCountRight != 0 {
		t.Errorf("门禁 envy: got left=%d want left=%d right=0",
			totalEnvy, golden.EnvyCountLeft)
	}
}

// TestBenchDeterminism 同参数两次运行逐位一致（RNG 流与排序全确定）。
func TestBenchDeterminism(t *testing.T) {
	r1, err := RunSuite(7, 0.24)
	if err != nil {
		t.Fatalf("RunSuite #1: %v", err)
	}
	r2, err := RunSuite(7, 0.24)
	if err != nil {
		t.Fatalf("RunSuite #2: %v", err)
	}
	for _, name := range append([]string{"classic", "drift", "cold"}, "market") {
		a, b := r1[name], r2[name]
		if a.HRAt3 != b.HRAt3 || a.NDCGAt5 != b.NDCGAt5 ||
			a.EnvyCountLeft != b.EnvyCountLeft || a.EnvyCountRight != b.EnvyCountRight {
			t.Errorf("%s 两次运行不一致: %+v vs %+v", name, a, b)
		}
	}
}

// TestBenchGatesPass 默认门禁通过（spec/03-oracles.md：HR@3≥0.6、
// NDCG@5≥0.4、总 envy≤2；三场景聚合 + market envy）。
func TestBenchGatesPass(t *testing.T) {
	reports, err := RunSuite(0, 0.24)
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	var scenarioReports []domain.EvaluationReport
	for _, name := range ScenarioNames {
		scenarioReports = append(scenarioReports, reports[name])
	}
	agg := AggregateReports(scenarioReports, ScenarioNames)
	totalEnvy := agg.TotalEnvy() + reports["market"].TotalEnvy()
	if agg.HRAt3 < 0.6 {
		t.Errorf("hr_at_3=%.3f 低于门禁 0.6", agg.HRAt3)
	}
	if agg.NDCGAt5 < 0.4 {
		t.Errorf("ndcg_at_5=%.3f 低于门禁 0.4", agg.NDCGAt5)
	}
	if totalEnvy > 2 {
		t.Errorf("total_envy=%d 超过门禁 2", totalEnvy)
	}
}

// TestBenchMarketConstructive 合成市场构造性 oracle：黄金对全部命中
// （HR@3 ≥ 0.99 由 RunBench 内部强制）+ envy-free。
func TestBenchMarketConstructive(t *testing.T) {
	report, err := RunBench(MarketOptions{Seed: 42})
	if err != nil {
		t.Fatalf("RunBench: %v", err)
	}
	if report.HRAt3 < 0.99 {
		t.Errorf("market HR@3=%.3f（构造性应≈1）", report.HRAt3)
	}
	if report.TotalEnvy() != 0 {
		t.Errorf("market envy=%d（对角市场应 envy-free）", report.TotalEnvy())
	}
}
