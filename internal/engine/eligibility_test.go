package engine

import (
	"testing"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// ExtractedSectionsAlias 本地别名（同型）。
type ExtractedSectionsAlias = domain.ExtractedSections

func secs(items map[string]string) map[domain.SectionName]string {
	out := map[domain.SectionName]string{}
	for k, v := range items {
		out[domain.SectionName(k)] = v
	}
	return out
}

// TestDetectHardConstraint 显式标记门控：只有"hard constraint/硬约束"
// 声明与规则触发词共现才认定约束；普通提及不触发（防误杀）。
func TestDetectHardConstraint(t *testing.T) {
	cases := []struct {
		name     string
		sections map[domain.SectionName]string
		wantKind string
		wantOK   bool
	}{
		{
			name: "en explicit",
			sections: secs(map[string]string{
				"needs": "ka retail entry, hard constraint: partner must have mainland china entity for compliance",
			}),
			wantKind: "geo_entity", wantOK: true,
		},
		{
			name: "zh explicit",
			sections: secs(map[string]string{
				"needs": "寻找代工产能，硬约束：合作方必须有中国大陆实体团队",
			}),
			wantKind: "geo_entity", wantOK: true,
		},
		{
			name: "local team",
			sections: secs(map[string]string{
				"needs": "on-site rollout, hard requirement: local team mandatory",
			}),
			wantKind: "local_team", wantOK: true,
		},
		{
			name: "mention without declarator",
			sections: secs(map[string]string{
				"needs": "we had a mainland china entity requirement discussion last year",
			}),
			wantOK: false,
		},
		{
			name: "declarator without rule marker",
			sections: secs(map[string]string{
				"needs": "hard constraint: budget above 500k",
			}),
			wantOK: false,
		},
		{
			name:     "clean profile",
			sections: secs(map[string]string{"needs": "need rust engineer", "skills": "python"}),
			wantOK:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, _, ok := DetectHardConstraint(tc.sections)
			if ok != tc.wantOK || (ok && kind != tc.wantKind) {
				t.Fatalf("got kind=%q ok=%v, want kind=%q ok=%v", kind, ok, tc.wantKind, tc.wantOK)
			}
		})
	}
}

// TestViolates 可见违反事实判定：counterpart 必须显式自述违反；
// 无自述放行（fail-safe：无证据不砍 pair）。
func TestViolates(t *testing.T) {
	violating := secs(map[string]string{
		"based_in": "Based in Singapore, fully remote delivery, no mainland China entity",
	})
	compliant := secs(map[string]string{
		"based_in": "Shanghai based team of 40, on-site across east china",
	})
	silent := secs(map[string]string{"skills": "ka retail execution, slotting negotiation"})

	if ok, hit := violates("geo_entity", violating); !ok {
		t.Fatalf("违反未检出（hit=%q）", hit)
	}
	if ok, _ := violates("geo_entity", compliant); ok {
		t.Fatal("合规方被误判违反")
	}
	if ok, _ := violates("geo_entity", silent); ok {
		t.Fatal("未自述事实的沉默方被误判违反（应放行交 LLM 层）")
	}

	if ok, _ := violates("local_team",
		secs(map[string]string{"needs": "硬约束：本地团队 mandatory",
			"other": "纯远程交付，无本地团队"})); !ok {
		t.Fatal("中文违反未检出")
	}
}

// TestEligibilityExclusions 双向构建：member 声明约束 + pool 可见违反
// → 该 pair 被排除（无论方向先后）。
func TestEligibilityExclusions(t *testing.T) {
	mk := func(id string, sections map[string]string) ExtractedSectionsAlias {
		return ExtractedSectionsAlias{ID: domain.UserID(id), Sections: secs(sections)}
	}
	members := []ExtractedSectionsAlias{
		mk("m1", map[string]string{"needs": "ka entry, hard constraint: mainland china entity required"}),
		mk("m2", map[string]string{"needs": "need fulfillment partner"}),
	}
	pool := []ExtractedSectionsAlias{
		mk("p1", map[string]string{"skills": "ka execution", "note": "no mainland entity, fully remote"}),
		mk("p2", map[string]string{"skills": "ka execution", "note": "shanghai entity, 20 staff"}),
	}
	excluded, n := EligibilityExclusions(members, pool)
	if n != 1 {
		t.Fatalf("排除数=%d want 1（%v）", n, excluded)
	}
	if !excluded[domain.StablePairID("m1", "p1")] {
		t.Fatal("违反对未被排除")
	}
	if excluded[domain.StablePairID("m2", "p1")] || excluded[domain.StablePairID("m2", "p2")] || excluded[domain.StablePairID("m1", "p2")] {
		t.Fatal("合法对被误排除")
	}
}
