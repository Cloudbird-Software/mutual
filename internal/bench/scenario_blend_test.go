package bench

import "testing"

// 本文件守护 ScenarioOptions 的 blending / 保底推荐选项（零值 = 现行
// 语义，由 bench_golden_test.go 逐位守护）。

// TestScenarioBlendRescuesParaphraseGap classic m3↔p3 黄金对的 B→A 方向
// 是同义改写（"teams drowning in cloud spend" vs "devops cicd observability
// prometheus"），纯方向分下 NSW 几何均值崩塌、m3 零匹配（HR@3=0.875）。
// embed 双信号混合（config/default.yaml 生产权重 0.35/0.65）应修复。
func TestScenarioBlendRescuesParaphraseGap(t *testing.T) {
	base, err := RunScenario("classic", ScenarioOptions{})
	if err != nil {
		t.Fatalf("基线 RunScenario: %v", err)
	}
	if base.HRAt3 >= 1.0 {
		t.Fatalf("前置失效：classic 基线已满分（HR@3=%.3f），失效场景被上游改掉", base.HRAt3)
	}
	got, err := RunScenario("classic", ScenarioOptions{EmbedWeight: 0.35, LLMWeight: 0.65})
	if err != nil {
		t.Fatalf("blend RunScenario: %v", err)
	}
	if got.HRAt3 < 1.0 {
		t.Fatalf("blend 未修复同义改写盲区: HR@3=%.3f（want 1.000）", got.HRAt3)
	}
}

// TestScenarioFallbackRescuesStarvation pool 竞争失利者（m3 的 p3 被 m9
// 抢占）在仅匹配边语义下零推荐。保底推荐用 PrefMatrix 行首候选补齐，
// 单独即可修复 classic（无需 blending）。
func TestScenarioFallbackRescuesStarvation(t *testing.T) {
	got, err := RunScenario("classic", ScenarioOptions{FallbackTopK: 3})
	if err != nil {
		t.Fatalf("fallback RunScenario: %v", err)
	}
	if got.HRAt3 < 1.0 {
		t.Fatalf("保底推荐未修复饿死: HR@3=%.3f（want 1.000）", got.HRAt3)
	}
}

// TestScenarioOptionsDeterminism 选项路径两次运行逐位一致（确定性契约
// 不因新选项破例）。
func TestScenarioOptionsDeterminism(t *testing.T) {
	opts := ScenarioOptions{EmbedWeight: 0.35, LLMWeight: 0.65, FallbackTopK: 3}
	a, err := RunScenario("classic", opts)
	if err != nil {
		t.Fatalf("第一次运行: %v", err)
	}
	b, err := RunScenario("classic", opts)
	if err != nil {
		t.Fatalf("第二次运行: %v", err)
	}
	if a.HRAt3 != b.HRAt3 || a.NDCGAt5 != b.NDCGAt5 || a.TotalEnvy() != b.TotalEnvy() {
		t.Fatalf("非确定: %+v vs %+v", a, b)
	}
}
