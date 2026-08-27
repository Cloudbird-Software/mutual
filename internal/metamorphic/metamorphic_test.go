package metamorphic

import (
	"encoding/json"
	"os"
	"testing"
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
