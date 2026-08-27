// eligibility.go 实现硬约束资格判定（匹配前的确定性二元门）。
//
// 动机（2026-08 实验 R8 建议 #4，docs/experiments/2026-08-synthetic-data.md）：
// 约束此前只存在于画像散文里，由 LLM 打分契约（baml v3 "hard constraints
// first" 封顶条款）做软防守——离线词法链路与未启用契约升级的部署完全
// 不设防，词面上吸引的违反者可以抢走合规黄金对的匹配容量。生产级
// 双边匹配需要显式二元资格位（对齐 holdout WorldResult.Eligible 语义）。
//
// 设计边界（fail-safe 优先）：
//   - 只认**显式标记**（"hard constraint:"/"硬约束"）声明的约束，
//     不做语义猜测——false positive 会静默砍掉合法 pair；
//   - 违反判定要求 counterpart **可见的自述事实**（"based in singapore,
//     no mainland entity"），无自述 = 无法判违反 = 放行（留给 LLM 层）；
//   - 覆盖跨境/招商域最高频的地理实体/本地驻场两族规则，
//     规则表扩充走测试先行（每族一条正反例）。
package engine

import (
	"strings"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// constraintRule 是一族硬约束的触发词与可见违反陈述。
type constraintRule struct {
	// markers 是约束声明里的触发词（须与显式标记共现才认定约束）。
	markers []string
	// violations 是 counterpart 文本中的可见违反事实。
	violations []string
	// kind 是规则族名（报告/理由用）。
	kind string
}

// constraintRules 规则表。扩充新族：补条目 + 补正反例测试。
var constraintRules = []constraintRule{
	{
		kind:    "geo_entity",
		markers: []string{"mainland china entity", "mainland entity", "大陆实体", "中国大陆实体", "实体团队"},
		violations: []string{
			"no mainland entity", "no mainland china entity", "fully remote",
			"remote delivery", "overseas delivery only", "无大陆实体", "纯远程", "远程交付",
		},
	},
	{
		kind:    "local_team",
		markers: []string{"local team mandatory", "local presence required", "本地团队", "驻场团队"},
		violations: []string{
			"fully remote", "no local presence", "no local team", "纯远程", "无本地团队", "无驻场",
		},
	},
}

// constraintDeclarators 是显式约束声明标记（必须与 rule marker 共现）。
var constraintDeclarators = []string{"hard constraint", "硬约束", "hard requirement", "mandatory:", "必须满足"}

// DetectHardConstraint 从画像分节检出显式硬约束（返回规则族与命中原文行）。
// 无约束返回 ok=false。大小写不敏感；逐行扫描（约束声明是行级语句）。
func DetectHardConstraint(sections map[domain.SectionName]string) (kind string, line string, ok bool) {
	for _, text := range sections {
		for _, rawLine := range strings.Split(text, "\n") {
			line = strings.TrimSpace(rawLine)
			if line == "" {
				continue
			}
			lower := strings.ToLower(line)
			declarative := false
			for _, d := range constraintDeclarators {
				if strings.Contains(lower, d) {
					declarative = true
					break
				}
			}
			if !declarative {
				continue
			}
			for _, rule := range constraintRules {
				for _, m := range rule.markers {
					if strings.Contains(lower, m) {
						return rule.kind, line, true
					}
				}
			}
		}
	}
	return "", "", false
}

// Violates 判定 counterpart 画像是否可见地违反本约束。
// 要求 counterpart 显式自述违反事实；无自述放行（交 LLM 层判断）。
func violates(kind string, counterpartSections map[domain.SectionName]string) (bool, string) {
	var rule *constraintRule
	for i := range constraintRules {
		if constraintRules[i].kind == kind {
			rule = &constraintRules[i]
			break
		}
	}
	if rule == nil {
		return false, ""
	}
	var all []string
	for _, text := range counterpartSections {
		all = append(all, strings.ToLower(text))
	}
	haystack := strings.Join(all, " ")
	for _, v := range rule.violations {
		if strings.Contains(haystack, v) {
			return true, v
		}
	}
	return false, ""
}

// EligibilityExclusions 双向构建不合格 pair 集：source 声明约束且
// target 可见违反（含反向）。返回稳定 PairID 集（直接可并入
// SelectPairs 的 excludedPairs，MR-8 守护其 honored 语义）与排除数。
func EligibilityExclusions(sources, targets []domain.ExtractedSections) (map[domain.PairID]bool, int) {
	excluded := map[domain.PairID]bool{}
	check := func(focal, counterpart domain.ExtractedSections) {
		kind, _, ok := DetectHardConstraint(focal.Sections)
		if !ok {
			return
		}
		if bad, _ := violates(kind, counterpart.Sections); bad {
			excluded[domain.StablePairID(focal.ID, counterpart.ID)] = true
		}
	}
	for _, s := range sources {
		for _, t := range targets {
			check(s, t)
			check(t, s)
		}
	}
	return excluded, len(excluded)
}
