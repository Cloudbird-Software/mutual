package bench

import "testing"

// 扩展陷阱套件守护（ExtendedScenarioNames，data/bench-extended）。
//
// 断言原则：下限防退化 + 注释记录实验基线。实验对照（2026-08 合成数据
// 实验，双标注者 MAE 0.023/0.040）：真实 LLM 信号在本套件全部场景
// HR@3=1.000——词法 surrogate 的失分是信号保真度差距，不是数据不可解。
// 详见 docs/experiments/2026-08-synthetic-data.md。

// TestExtendedParaphrase 同义改写盲区：黄金对的关键链路词面零重叠
// （如 撮合 tail latency ↔ protocol pacing/jitter taming）。
//
// 实验基线：纯方向分 HR@3=0.125；任何 embed/llm 词法混合组合不可救
// （0.000-0.125）；真实 LLM 标注信号 1.000。本场景是"只有语义信号
// 能解"的天花板测量——断言下限防的是 blending/fallback 把词法基线
// 进一步打穿。
func TestExtendedParaphrase(t *testing.T) {
	base, err := RunExtendedScenario("paraphrase", ScenarioOptions{})
	if err != nil {
		t.Fatalf("基线: %v", err)
	}
	if base.HRAt3 < 0.125 {
		t.Fatalf("同义改写场景词法基线退化: HR@3=%.3f（实验基线 0.125）", base.HRAt3)
	}
	tuned, err := RunExtendedScenario("paraphrase", ScenarioOptions{EmbedWeight: 0.35, LLMWeight: 0.65, FallbackTopK: 3})
	if err != nil {
		t.Fatalf("blend+fallback: %v", err)
	}
	if tuned.HRAt3 < 0.125 {
		t.Fatalf("blend+fallback 低于词法基线: HR@3=%.3f", tuned.HRAt3)
	}
}

// TestExtendedScenarioRejectsUnknownName 扩展加载器白名单（fail-closed）。
func TestExtendedScenarioRejectsUnknownName(t *testing.T) {
	if _, err := LoadExtendedScenario("classic", ""); err == nil {
		t.Fatal("官方场景不得从扩展目录加载")
	}
	if _, err := RunExtendedScenario("nope", ScenarioOptions{}); err == nil {
		t.Fatal("未知扩展场景应报错")
	}
}
