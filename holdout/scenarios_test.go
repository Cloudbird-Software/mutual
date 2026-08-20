package holdout

// 12 个人工编写的业务陷阱场景（HT-01..HT-12）。
// 数据在 scenarios/HT-*.json，期望行为由人类写死。runner 只做求值，不含场景知识。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type scenario struct {
	ID         string            `json:"id"`
	Title      string            `json:"title"`
	Intent     string            `json:"intent"`
	Profiles   map[string]string `json:"profiles"`
	Assertions []assertion       `json:"assertions"`
}

type assertion struct {
	Type        string   `json:"type"`
	Focal       string   `json:"focal"`
	Counterpart string   `json:"counterpart"`
	Expected    *bool    `json:"expected"`
	Keywords    []string `json:"keywords"`
	Max         float64  `json:"max"`
	Min         float64  `json:"min"`
	Pair        []string `json:"pair"`
	Agent       string   `json:"agent"`
	Why         string   `json:"why"`
}

func checkAssertion(res WorldResult, a assertion) error {
	why := a.Why
	switch a.Type {
	case "eligible":
		got := res.IsEligible(a.Focal, a.Counterpart)
		if a.Expected == nil || got != *a.Expected {
			return fmt.Errorf("eligible 期望 %v 实得 %v｜%s", a.Expected, got, why)
		}
	case "reason_contains":
		r := strings.ToLower(res.ReasonOf(a.Focal, a.Counterpart))
		for _, k := range a.Keywords {
			if strings.Contains(r, strings.ToLower(k)) {
				return nil
			}
		}
		return fmt.Errorf("reason %q 未命中 %v｜%s", r, a.Keywords, why)
	case "level_le":
		if lv := float64(res.LevelOf(a.Focal, a.Counterpart)); lv > a.Max {
			return fmt.Errorf("level=%v > %v｜%s", lv, a.Max, why)
		}
	case "level_ge":
		if lv := float64(res.LevelOf(a.Focal, a.Counterpart)); lv < a.Min {
			return fmt.Errorf("level=%v < %v｜%s", lv, a.Min, why)
		}
	case "confidence_le":
		if cf := res.ConfOf(a.Focal, a.Counterpart); cf > a.Max {
			return fmt.Errorf("confidence=%v > %v｜%s", cf, a.Max, why)
		}
	case "matched":
		if !res.IsMatched(a.Pair[0], a.Pair[1]) {
			return fmt.Errorf("%s-%s 应被匹配｜%s", a.Pair[0], a.Pair[1], why)
		}
	case "not_matched":
		if res.IsMatched(a.Pair[0], a.Pair[1]) {
			return fmt.Errorf("%s-%s 不应被匹配｜%s", a.Pair[0], a.Pair[1], why)
		}
	case "degree_le":
		if d := float64(res.Degree(a.Agent)); d > a.Max {
			return fmt.Errorf("%s degree=%v > %v｜%s", a.Agent, d, a.Max, why)
		}
	default:
		return fmt.Errorf("未知断言类型: %s", a.Type)
	}
	return nil
}

func TestTrapScenarios(t *testing.T) {
	requireUnlock(t)
	paths, err := filepath.Glob(filepath.Join("scenarios", "HT-*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("未找到场景文件（scenarios/HT-*.json）: err=%v", err)
	}
	sort.Strings(paths)
	for _, p := range paths {
		p := p
		t.Run(filepath.Base(p), func(t *testing.T) {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			var sc scenario
			if err := json.Unmarshal(data, &sc); err != nil {
				t.Fatalf("场景 JSON 解析失败: %v", err)
			}
			res := runWorld(t, sc.Profiles)
			for i, a := range sc.Assertions {
				if err := checkAssertion(res, a); err != nil {
					t.Errorf("断言 %d 失败: %v", i, err)
				}
			}
		})
	}
}
