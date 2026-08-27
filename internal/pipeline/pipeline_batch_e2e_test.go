package pipeline

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/Cloudbird-Software/mutual/config"
	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/engine"
	"github.com/Cloudbird-Software/mutual/internal/rng"
	"github.com/Cloudbird-Software/mutual/internal/signal"
)

// ---------------------------------------------------------------------------
// 二部 e2e（生产级遗留项 #1）：RunBatchMatch 全链路（extract→hyde→embed→
// similarity→select→score→solve）+ 硬约束资格过滤端到端。替身语义：
// extract=解析 prompt 中的分节行；score=DirectionalScore+pair 种子噪声；
// embed=signal.FakeEmbedder（内容寻址，确定性）。
// ---------------------------------------------------------------------------

type batchLLM struct {
	sections  map[string]map[string]string
	scoreHits int
}

var (
	batchPairRE  = regexp.MustCompile(`### Pair \d+: \(([A-Za-z0-9_]+), ([A-Za-z0-9_]+)\)`)
	batchExtract = regexp.MustCompile(`(?m)^([a-z]+): (.+)$`)
)

func (b *batchLLM) CompleteExtract(prompt string, model string) (string, error) {
	sec := map[string]string{}
	for _, m := range batchExtract.FindAllStringSubmatch(prompt, -1) {
		key := m[1]
		if key == "skills" || key == "vision" || key == "project" || key == "needs" {
			sec[key] = strings.TrimSpace(m[2])
		}
	}
	out, err := json.Marshal(sec)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (b *batchLLM) CompleteHyde(prompt string, model string) (string, error) {
	return `["seeking complementary capability partners"]`, nil
}

func (b *batchLLM) CompleteIntroduce(prompt string, model string) (string, error) {
	return `{"intro": "batch e2e intro", "starter_topics": "topic"}`, nil
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func (b *batchLLM) CompleteScore(prompt string, model string) (string, error) {
	b.scoreHits++
	locs := batchPairRE.FindAllStringSubmatch(prompt, -1)
	if len(locs) == 0 {
		return `{"a_to_b": 0.5, "b_to_a": 0.5, "reasoning": "fallback"}`, nil
	}
	var items []string
	for _, m := range locs {
		u1, u2 := m[1], m[2]
		s1, s2 := b.sections[u1], b.sections[u2]
		a := signal.DirectionalScore(s1, s2)
		c := signal.DirectionalScore(s2, s1)
		h := rng.New(hashStr(u1 + "__" + u2))
		a = clamp01(a + 0.24*(h.Float64()-0.5))
		c = clamp01(c + 0.24*(h.Float64()-0.5))
		items = append(items, fmt.Sprintf(`{"a_to_b": %.4f, "b_to_a": %.4f, "reasoning": "stub"}`, a, c))
	}
	return "[" + strings.Join(items, ",") + "]", nil
}

func hashStr(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// batchProfiles 是跨境招商混合语料：m1 声明地理实体硬约束；pX1 词面
// 回声约束词但可见违反（no mainland entity, fully remote）；m2..m4
// 无约束，各有合规黄金对。
func batchProfiles() map[string]map[string]string {
	return map[string]map[string]string{
		"m1": {"needs": "ka retail entry execution, hard constraint: mainland china entity required",
			"project": "offline expansion 300 doors", "skills": "brand marketing category strategy",
			"vision": "shelf presence in top chains"},
		"m2": {"needs": "customs clearance broker for imports", "project": "import doubling",
			"skills": "sourcing procurement", "vision": "smooth border flow"},
		"m3": {"needs": "livestream commerce team for launches", "project": "monthly launches",
			"skills": "ecommerce analytics", "vision": "conversion growth"},
		"m4": {"needs": "overseas warehouse for returns", "project": "reverse logistics setup",
			"skills": "inventory planning", "vision": "flexible fulfillment"},
		"p1": {"needs": "brands seeking offline growth", "project": "ka entry service east china",
			"skills": "ka retail entry execution slotting negotiation", "vision": "brands on shelves",
			"note": "shanghai based entity 40 staff"},
		"pX1": {"needs": "brands to expand offline", "project": "retail entry advisory",
			"skills": "ka retail entry execution mainland china entity advisory", "vision": "brands on shelves",
			"note": "no mainland entity, fully remote delivery"},
		"p2": {"needs": "importers needing border services", "project": "customs brokerage desk",
			"skills": "customs clearance broker hs codes", "vision": "smooth border flow"},
		"p3": {"needs": "brands launching products", "project": "livestream studio",
			"skills": "livestream commerce hosts conversion", "vision": "conversion growth"},
		"p4": {"needs": "sellers with return volume", "project": "returns processing center",
			"skills": "overseas warehouse returns processing", "vision": "flexible fulfillment"},
		"p5": {"needs": "brands needing content", "project": "short video ops",
			"skills": "content marketing seeding", "vision": "attention economy"},
		"p6": {"needs": "sellers needing sourcing", "project": "sourcing desk",
			"skills": "supplier audit negotiation", "vision": "supply resilience"},
		"p7": {"needs": "teams needing planning tools", "project": "inventory saas",
			"skills": "forecasting software implementation", "vision": "operational excellence"},
	}
}

func setupBatch(t *testing.T, overrides map[string]any) (*BatchMatchResult, *batchLLM, map[string]map[string]string) {
	t.Helper()
	profiles := batchProfiles()
	sections := map[string]map[string]string{}
	var extracted []domain.ExtractedSections
	for id, sec := range profiles {
		sections[id] = sec
		sm := map[domain.SectionName]string{}
		for k, v := range sec {
			sm[domain.SectionName(k)] = v
		}
		extracted = append(extracted, domain.NewExtractedSections(domain.UserID(id), sm, ""))
	}
	cfg, err := config.Load("", overrides)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	bundle, err := engine.EmbedSections(extracted, map[domain.UserID]domain.HydeDescriptors{}, "stub-model", nil, signal.FakeEmbedder{})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	llm := &batchLLM{sections: sections}
	result, err := RunBatchMatch(BatchMatchInput{
		MemberIDs:   []domain.UserID{"m1", "m2", "m3", "m4"},
		PoolBundle:  bundle,
		PoolSections: extracted,
	}, cfg, Deps{LLM: llm, Embedder: signal.FakeEmbedder{}})
	if err != nil {
		t.Fatalf("RunBatchMatch: %v", err)
	}
	return result, llm, sections
}

// TestBatchMatchE2E 二部全链路：member 侧范围、黄金对命中、确定性。
func TestBatchMatchE2E(t *testing.T) {
	result, _, _ := setupBatch(t, map[string]any{
		"matching.hard_constraint_filter": false,
		"matching.b_max":                  2,
		"matching.pool_b_max":             nil,
	})
	if len(result.MemberIDs) != 4 || len(result.PoolIDs) != 12 {
		t.Fatalf("batch 元数据: members=%d pools=%d", len(result.MemberIDs), len(result.PoolIDs))
	}
	memberSet := map[domain.UserID]bool{}
	for _, id := range result.MemberIDs {
		memberSet[id] = true
	}
	for _, e := range result.MatchResult.Edges {
		if !memberSet[e.User1] && !memberSet[e.User2] {
			t.Fatalf("边 %s 超出 member 侧报告范围", e.PairID)
		}
	}
	// 黄金对（无约束成员）应被匹配
	golds := [][2]string{{"m2", "p2"}, {"m3", "p3"}, {"m4", "p4"}}
	for _, g := range golds {
		found := false
		for _, e := range result.MatchResult.Edges {
			if (e.User1 == domain.UserID(g[0]) && e.User2 == domain.UserID(g[1])) ||
				(e.User1 == domain.UserID(g[1]) && e.User2 == domain.UserID(g[0])) {
				found = true
			}
		}
		if !found {
			t.Errorf("黄金对 %s-%s 未被匹配", g[0], g[1])
		}
	}
	// 确定性：同输入两次运行边集语义一致
	result2, _, _ := setupBatch(t, map[string]any{
		"matching.hard_constraint_filter": false,
		"matching.b_max":                  2,
		"matching.pool_b_max":             nil,
	})
	if len(result.MatchResult.Edges) != len(result2.MatchResult.Edges) {
		t.Fatalf("两次运行边数不一致: %d vs %d", len(result.MatchResult.Edges), len(result2.MatchResult.Edges))
	}
	for i := range result.MatchResult.Edges {
		if result.MatchResult.Edges[i].PairID != result2.MatchResult.Edges[i].PairID {
			t.Fatalf("第 %d 条边不一致: %s vs %s", i, result.MatchResult.Edges[i].PairID, result2.MatchResult.Edges[i].PairID)
		}
	}
}

// TestBatchMatchEligibility 硬约束资格过滤端到端：m1 的约束 + pX1 的
// 可见违反 → 该对不产生边、不耗 LLM 预算（前置于候选选择）、有 notes。
func TestBatchMatchEligibility(t *testing.T) {
	result, llm, _ := setupBatch(t, map[string]any{
		"matching.hard_constraint_filter": true,
		"matching.b_max":                  2,
	})
	for _, e := range result.MatchResult.Edges {
		pair := fmt.Sprintf("%s/%s", e.User1, e.User2)
		if e.User1 == domain.UserID("m1") && e.User2 == domain.UserID("pX1") ||
			e.User1 == domain.UserID("pX1") && e.User2 == domain.UserID("m1") {
			t.Fatalf("违反硬约束的 pair 被匹配: %s", pair)
		}
	}
	if n, _ := result.Metadata["n_ineligible_pairs"].(int); n < 1 {
		t.Fatalf("n_ineligible_pairs=%v want ≥1", result.Metadata["n_ineligible_pairs"])
	}
	notes, _ := result.MatchResult.ReportData["notes"].([]string)
	foundNote := false
	for _, nt := range notes {
		if strings.Contains(nt, "硬约束") {
			foundNote = true
		}
	}
	if !foundNote {
		t.Fatalf("报告缺少资格过滤 note: %v", notes)
	}
	if llm.scoreHits == 0 {
		t.Fatal("LLM 打分未被调用（替身接线失效）")
	}
}
