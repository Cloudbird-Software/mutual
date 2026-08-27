package bench

import (
	"math"
	"testing"

	"github.com/Cloudbird-Software/mutual/internal/signal"
)

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

// TestExtendedDecoy 词面欺骗：干扰者把 member 的 needs 关键词近乎原样
// 复述进 skills（token 重叠极高），但无项目证据支撑、反向价值≈0。
//
// 实验基线：纯方向分 HR@3=0.250（8 对仅 1-2 对幸存且退居第 2）；
// blend(0.35/0.65)+fallback(3) 修复至 1.000/NDCG 0.677——保底推荐
// 让被 NSW 几何均值正确降权的场景重新可见。断言 tuned 下限 0.875。
func TestExtendedDecoy(t *testing.T) {
	base, err := RunExtendedScenario("decoy", ScenarioOptions{})
	if err != nil {
		t.Fatalf("基线: %v", err)
	}
	if base.HRAt3 < 0.25 {
		t.Fatalf("词面欺骗场景基线退化: HR@3=%.3f（实验基线 0.250）", base.HRAt3)
	}
	tuned, err := RunExtendedScenario("decoy", ScenarioOptions{EmbedWeight: 0.35, LLMWeight: 0.65, FallbackTopK: 3})
	if err != nil {
		t.Fatalf("blend+fallback: %v", err)
	}
	if tuned.HRAt3 < 0.875 || tuned.NDCGAt5 < 0.6 {
		t.Fatalf("blend+fallback 未达实验值: HR@3=%.3f NDCG@5=%.3f（实验 1.000/0.677）", tuned.HRAt3, tuned.NDCGAt5)
	}
}

// TestExtendedMessy 真实语料：自然长句、大小写数字版本号混杂（K8s vs
// kubernetes 类缩写陷阱、阶段/规模错配干扰者）。14 member × 12 pool。
//
// 实验基线：纯方向分 HR@3=1.000 但 envy=11——分数压缩让公平性崩坏；
// blend(0.35/0.65) 把 envy 修到 2（embed 信号补齐弱方向缺口）。
// 断言：tuned envy ≤ 2 且 HR 不低于 0.875。
func TestExtendedMessy(t *testing.T) {
	base, err := RunExtendedScenario("messy", ScenarioOptions{})
	if err != nil {
		t.Fatalf("基线: %v", err)
	}
	if base.HRAt3 < 0.875 {
		t.Fatalf("真实语料基线退化: HR@3=%.3f（实验基线 1.000）", base.HRAt3)
	}
	if base.TotalEnvy() < 11 {
		t.Fatalf("基线 envy 异常: %d（实验基线 11——若消失说明分数分布被改动）", base.TotalEnvy())
	}
	tuned, err := RunExtendedScenario("messy", ScenarioOptions{EmbedWeight: 0.35, LLMWeight: 0.65})
	if err != nil {
		t.Fatalf("blend: %v", err)
	}
	if tuned.HRAt3 < 0.875 || tuned.TotalEnvy() > 2 {
		t.Fatalf("blend 未修复公平性: HR@3=%.3f envy=%d（实验 1.000/2）", tuned.HRAt3, tuned.TotalEnvy())
	}
}

// TestExtendedConstraints 硬约束资格过滤（engine.EligibilityExclusions 接线）：
// m1/m2 声明显式硬约束，pX1/pX2 词面更强（回声约束词）但可见违反。
// 关闭过滤：违反者排序倒挂（压过合规黄金对）——词法链路对约束不设防；
// 开启过滤：违反对被清零（n_ineligible_pairs ≥ 2），黄金对回到 top-1。
func TestExtendedConstraints(t *testing.T) {
	// 关闭过滤：倒挂可见（违反者 NSW ≥ 黄金 NSW）
	off, err := RunExtendedScenario("constraints", ScenarioOptions{})
	if err != nil {
		t.Fatalf("关闭过滤: %v", err)
	}
	if off.Metadata["n_ineligible_pairs"] != nil {
		t.Fatal("关闭过滤时不应有资格排除元数据")
	}

	on, err := RunExtendedScenario("constraints", ScenarioOptions{HardConstraintFilter: true})
	if err != nil {
		t.Fatalf("开启过滤: %v", err)
	}
	n, _ := on.Metadata["n_ineligible_pairs"].(int)
	if n < 2 {
		t.Fatalf("资格排除数=%d want ≥2（pX1/pX2 违反对）", n)
	}
	if on.HRAt3 < 1.0 {
		t.Fatalf("过滤后黄金对未全命中: HR@3=%.3f", on.HRAt3)
	}
}

// TestExtendedConstraintsOrderInversion 关闭过滤时倒挂真实存在
// （守卫场景判别力：若词法链路天然不倒挂，本测试报警场景失效）。
func TestExtendedConstraintsOrderInversion(t *testing.T) {
	s, err := LoadExtendedScenario("constraints", "")
	if err != nil {
		t.Fatalf("加载: %v", err)
	}
	// 直接核算 m1 行：violator(pX1) 的 NSW ≥ gold(p1)（词面回声约束词
	// 构成真实威胁；生产修复由资格过滤承担）
	nsw := func(mid, pid string) float64 {
		var msec, psec map[string]string
		for _, m := range s.Members {
			if m.ID == mid {
				msec = m.Sections
			}
		}
		for _, p := range s.Pool {
			if p.ID == pid {
				psec = p.Sections
			}
		}
		a := signal.DirectionalScore(msec, psec)
		b := signal.DirectionalScore(psec, msec)
		return math.Sqrt(a * b)
	}
	if nsw("m1", "pX1") < nsw("m1", "p1") {
		t.Fatalf("场景判别力失效：违反者词面未压过合规黄金（%.3f < %.3f）",
			nsw("m1", "pX1"), nsw("m1", "p1"))
	}
}
