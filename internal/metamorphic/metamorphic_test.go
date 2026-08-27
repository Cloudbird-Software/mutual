package metamorphic

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/Cloudbird-Software/mutual/internal/signal"
)

// loadFile 从 data 目录加载场景文件（official 三场景 + 扩展陷阱套件）。
func loadFile(t *testing.T, path string) *Scenario {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s: %v", path, err)
	}
	var doc struct {
		Members     json.RawMessage   `json:"members"`
		Pool        json.RawMessage   `json:"pool"`
		GroundTruth map[string]string `json:"ground_truth"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
	s, err := ParseScenario(doc.Members, doc.Pool, doc.GroundTruth)
	if err != nil {
		t.Fatalf("ParseScenario %s: %v", path, err)
	}
	return s
}

// TestMetamorphicSuite 全场景全 MR 硬断言（CI 常驻）。
// 场景覆盖：官方三场景（词面直给/兴趣演化/冷启动）+ 扩展陷阱套件
// （同义改写/词面欺骗/真实语料）。
func TestMetamorphicSuite(t *testing.T) {
	scenarios := []struct {
		name string
		path string
	}{
		{"classic", "../../data/bench/classic.json"},
		{"drift", "../../data/bench/drift.json"},
		{"cold", "../../data/bench/cold.json"},
		{"paraphrase", "../../data/bench-extended/paraphrase.json"},
		{"decoy", "../../data/bench-extended/decoy.json"},
		{"messy", "../../data/bench-extended/messy.json"},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			s := loadFile(t, sc.path)
			for _, r := range RunSuite(s) {
				if !r.Pass {
					t.Errorf("%s: %s — %s", sc.name, r.Name, r.Detail)
				} else {
					t.Logf("%s: %s — %s", sc.name, r.Name, r.Detail)
				}
			}
		})
	}
}

// TestLadderOracle 已知值阶梯独立断言（构造性真值：重叠 k 单调 →
// NSW 严格单调；LLM 冷上下文评审确认阶梯在语义层同样单调 0→1）。
func TestLadderOracle(t *testing.T) {
	nsw := ladderNSW()
	for i := 1; i < len(nsw); i++ {
		if nsw[i] <= nsw[i-1] {
			t.Fatalf("阶梯非单调: nsw=%v", nsw)
		}
	}
}

// TestMetamorphicCJK 中文画像的 MR 守护（跨语言盲区修复的回归防线：
// CJK 二元组让中文侧信号可观测，MR 全链路在双语语料上成立）。
func TestMetamorphicCJK(t *testing.T) {
	s := &Scenario{
		Members: []signal.OrderedSections{
			{ID: "zm0", Sections: map[string]string{
				"needs": "急需金融科技方向的清算能力合作伙伴", "project": "金融科技项目",
				"skills": "清算落地经验十年", "vision": "金融科技长期主义"}},
			{ID: "zm1", Sections: map[string]string{
				"needs": "寻找精密制造质检体系辅导", "project": "制造项目",
				"skills": "数控加工产能", "vision": "精密制造长期主义"}},
		},
		Pool: []signal.OrderedSections{
			{ID: "zp0", Sections: map[string]string{
				"needs": "寻找金融科技产业链伙伴", "project": "金融服务包",
				"skills": "提供清算结算全套方案", "vision": "金融科技长期主义"}},
			{ID: "zp1", Sections: map[string]string{
				"needs": "需要制造企业客户", "project": "质检服务包",
				"skills": "质检体系认证辅导团队", "vision": "精密制造长期主义"}},
		},
		GroundTruth: map[string]string{"zm0": "zp0", "zm1": "zp1"},
	}
	for _, r := range RunSuite(s) {
		if !r.Pass {
			t.Errorf("%s — %s", r.Name, r.Detail)
		}
	}
}
