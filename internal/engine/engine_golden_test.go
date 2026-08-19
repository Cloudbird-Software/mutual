package engine

import (
	"encoding/json"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// golden/engine/full_flow.json 由 scripts/capture_golden_engine.py 从
// Python 基线捕获（golden embedder + fake_llm 契约），覆盖
// similarity → select → score → pre_matrix → match → report 全链路。
// 本测试逐位对拍——Go 重写等价性的核心证据。
type goldenFlow struct {
	Bundle struct {
		UserIDs      []string         `json:"user_ids"`
		SectionNames []string         `json:"section_names"`
		Dim          int              `json:"dim"`
		Embeddings   [][][]float64    `json:"embeddings"`
		HydeShapes   map[string][]int `json:"hyde_shapes"`
	} `json:"bundle"`
	Similarity struct {
		DirMatrix   [][]float64 `json:"dir_matrix"`
		FusedMatrix [][]float64 `json:"fused_matrix"`
	} `json:"similarity"`
	SelectedPairs []struct {
		User1           string  `json:"user1"`
		User2           string  `json:"user2"`
		PairID          string  `json:"pair_id"`
		SimilarityScore float64 `json:"similarity_score"`
	} `json:"selected_pairs"`
	PairScores      map[string]map[string]any `json:"pair_scores"`
	PairScoreOrder  []string                  `json:"pair_score_order"`
	UnscoredPairIDs []string                  `json:"unscored_pair_ids"`
	PrefMatrix      struct {
		LeftIDs         []string    `json:"left_ids"`
		RightIDs        []string    `json:"right_ids"`
		PrefLeftToRight [][]float64 `json:"pref_left_to_right"`
		PrefRightToLeft [][]float64 `json:"pref_right_to_left"`
	} `json:"pref_matrix"`
	Match struct {
		Edges      []map[string]any `json:"edges"`
		MatchProb  [][]float64      `json:"match_prob"`
		EnvyReport map[string]any   `json:"envy_report"`
	} `json:"match"`
	Report map[string]any `json:"report"`
}

func loadGoldenFlow(t *testing.T) *goldenFlow {
	t.Helper()
	raw, err := os.ReadFile("../../golden/engine/full_flow.json")
	if err != nil {
		t.Fatalf("读取 golden engine 向量失败: %v", err)
	}
	var g goldenFlow
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("解析 golden engine 向量失败: %v", err)
	}
	return &g
}

// goldenBundle 从 golden JSON 重建 EmbeddingsBundle。
func goldenBundle(t *testing.T, g *goldenFlow) *domain.EmbeddingsBundle {
	t.Helper()
	b := &domain.EmbeddingsBundle{
		EmbeddingModel: "golden-embedder",
		Dim:            g.Bundle.Dim,
		Hyde:           map[domain.SectionName][][]domain.Vector{},
	}
	for _, u := range g.Bundle.UserIDs {
		b.UserIDs = append(b.UserIDs, domain.UserID(u))
	}
	for _, n := range g.Bundle.SectionNames {
		b.SectionNames = append(b.SectionNames, domain.SectionName(n))
		// hyde 形状 [N, 0, D]（golden embedder 不产描述符）。
		b.Hyde[domain.SectionName(n)] = make([][]domain.Vector, len(b.UserIDs))
	}
	// golden embedder 无 hyde 描述符：每分节恰一个向量（[user][section][dim]）。
	for _, user := range g.Bundle.Embeddings {
		var ue domain.UserEmbeddings
		for _, section := range user {
			v := make(domain.Vector, len(section))
			copy(v, section)
			ue = append(ue, domain.SectionEmbeddings{v})
		}
		b.Embeddings = append(b.Embeddings, ue)
	}
	return b
}

// goldenRecipe 镜像 config/default.yaml 的 recipe 段。
func goldenRecipe() RecipeConfig {
	return RecipeConfig{
		SectionWeights: map[string]float64{
			"skills":  -0.10,
			"vision":  0.35,
			"project": 0.25,
			"needs":   -0.10,
		},
		CrossSectionWeights: []WeightEntry{
			{Key: "needs_skills", Value: 0.80},
		},
	}
}

// fakeLLM 复刻 tests/conftest.py 的打分路由（spec/04-fixtures.md §7.1）。
type fakeLLM struct{}

var fakeScoreTable = map[string][2]float64{
	"alice__bob":   {0.85, 0.90},
	"alice__carol": {0.80, 0.82},
	"bob__carol":   {0.83, 0.82},
	"alice__david": {0.52, 0.63},
	"bob__david":   {0.45, 0.58},
	"carol__david": {0.35, 0.65},
}

var fakeCohortIDs = []string{"alice", "bob", "carol", "david"}

// 按阶段类型化分发（§7.1 语义的 Go 落地：打分 → 查表，其余 → 固定话术）。
//
// 批量契约（CodeRabbit）：prompt 含 "### Pair N: (u1, u2)" 分块时逐块
// 查表、按块序返回 JSON 数组（batch>1 的 parseScoringResponse 只接受
// 数组）；单块保持单对象（golden 捕获用 batch=1，逐位不变）。
func (fakeLLM) CompleteScore(prompt string, model string) (string, error) {
	_ = model
	var blocks [][]string
	for _, loc := range goldenPairHeaderRE.FindAllStringSubmatch(prompt, -1) {
		blocks = append(blocks, loc[1:])
	}
	if len(blocks) == 0 {
		return goldenWholePromptScore(prompt), nil
	}
	objs := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		ids := []string{b[0], b[1]}
		sort.Strings(ids)
		obj := map[string]any{"a_to_b": 0.5, "b_to_a": 0.5, "reasoning": "fake"}
		if entry, ok := fakeScoreTable[ids[0]+"__"+ids[1]]; ok {
			obj["a_to_b"], obj["b_to_a"] = entry[0], entry[1]
		}
		objs = append(objs, obj)
	}
	if len(objs) == 1 {
		out, _ := json.Marshal(objs[0])
		return string(out), nil
	}
	out, _ := json.Marshal(objs)
	return string(out), nil
}

// goldenPairHeaderRE 匹配批量打分 prompt 的分对块头。
var goldenPairHeaderRE = regexp.MustCompile(`(?m)^### Pair \d+: \(([^,\s]+), ([^)\s]+)\)$`)

// goldenWholePromptScore 整段查表（非批量 prompt 的旧路径）。
func goldenWholePromptScore(prompt string) string {
	var found []string
	for _, id := range fakeCohortIDs {
		if strings.Contains(prompt, id) {
			found = append(found, id)
		}
	}
	if len(found) >= 2 {
		if entry, ok := fakeScoreTable[found[0]+"__"+found[1]]; ok {
			out, _ := json.Marshal(map[string]any{
				"a_to_b": entry[0], "b_to_a": entry[1], "reasoning": "fake",
			})
			return string(out)
		}
	}
	return `{"a_to_b": 0.5, "b_to_a": 0.5, "reasoning": "fake"}`
}

func (fakeLLM) CompleteExtract(prompt string, model string) (string, error) {
	_ = prompt
	_ = model
	return `{"intro": "Fake intro.", "starter_topics": "Fake topic."}`, nil
}

func (fakeLLM) CompleteHyde(prompt string, model string) (string, error) {
	_ = prompt
	_ = model
	return `{"intro": "Fake intro.", "starter_topics": "Fake topic."}`, nil
}

func (fakeLLM) CompleteIntroduce(prompt string, model string) (string, error) {
	_ = prompt
	_ = model
	return `{"intro": "Fake intro.", "starter_topics": "Fake topic."}`, nil
}

// sectionsDict 从 golden profile 重建打分用 sections（golden 约定：
// extract 直接用画像自带 sections）。为避免依赖 fixture 文件，
// 分节文本用 golden embedder 的 user_id 占位（fake 路由只看 id）。
func goldenSectionsDict(g *goldenFlow) map[domain.UserID]map[string]string {
	out := map[domain.UserID]map[string]string{}
	for _, uid := range g.Bundle.UserIDs {
		sections := map[string]string{}
		for _, name := range g.Bundle.SectionNames {
			sections[name] = uid + " " + name
		}
		out[domain.UserID(uid)] = sections
	}
	return out
}

// assertMatrixEqual 矩阵对拍：容忍 ≤1e-12 的相对误差。
//
// 为什么不是逐位一致：dir/fused/pref 矩阵由 cosine 点积驱动，
// Python 侧 numpy.einsum 走 SIMD+FMA 分块求和（顺序因 CPU 后端
// 而异），Go 侧为顺序求和——两者存在 ≤ 数 ulp 的系统性差异，
// 且无法跨机器稳定复刻（internal/num 包文档记录了这一边界）。
// 差异不会传导：下游所有 dict 输出（pair_scores/edges/report）
// 一律 round(x, 3) 后**逐位**比较（assertJSONValueEqual）。
func assertMatrixEqual(t *testing.T, name string, got, want [][]float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s 行数: got %d want %d", name, len(got), len(want))
	}
	for i := range got {
		if len(got[i]) != len(want[i]) {
			t.Fatalf("%s 第 %d 行列数: got %d want %d", name, i, len(got[i]), len(want[i]))
		}
		for j := range got[i] {
			if got[i][j] != want[i][j] {
				tol := 1e-12 * math.Max(1, math.Abs(want[i][j]))
				if diff := math.Abs(got[i][j] - want[i][j]); diff > tol {
					t.Errorf("%s[%d][%d]: got %v want %v (diff %g > tol %g)", name, i, j, got[i][j], want[i][j], diff, tol)
				}
			}
		}
	}
}

func TestGoldenSimilarity(t *testing.T) {
	g := loadGoldenFlow(t)
	b := goldenBundle(t, g)
	sim := ComputeSimilarity(b, nil, goldenRecipe())
	assertMatrixEqual(t, "dir_matrix", sim.DirMatrix, g.Similarity.DirMatrix)
	assertMatrixEqual(t, "fused_matrix", sim.FusedMatrix, g.Similarity.FusedMatrix)
}

func TestGoldenSelect(t *testing.T) {
	g := loadGoldenFlow(t)
	b := goldenBundle(t, g)
	sim := ComputeSimilarity(b, nil, goldenRecipe())
	// 镜像 default.yaml budgets（cap 足够大，全选）。
	cap24, cap1200 := 24, 1200
	selected := SelectPairs(sim, SelectBudgets{PerProfileCap: &cap24, GlobalCap: &cap1200}, nil)
	if len(selected) != len(g.SelectedPairs) {
		t.Fatalf("selected 数: got %d want %d", len(selected), len(g.SelectedPairs))
	}
	for i, pair := range selected {
		want := g.SelectedPairs[i]
		if string(pair.User1) != want.User1 || string(pair.User2) != want.User2 ||
			string(pair.PairID) != want.PairID {
			t.Errorf("selected[%d]: got %+v want %+v", i, pair, want)
		}
		// similarity_score 来自 fused 矩阵（einsum ulp 级差异，见
		// assertMatrixEqual 的说明）：容差比较。
		if diff := math.Abs(pair.SimilarityScore - want.SimilarityScore); diff > 1e-12 {
			t.Errorf("selected[%d] score: got %v want %v", i, pair.SimilarityScore, want.SimilarityScore)
		}
	}
}

func TestGoldenScoreAndMatchFlow(t *testing.T) {
	g := loadGoldenFlow(t)
	b := goldenBundle(t, g)
	sim := ComputeSimilarity(b, nil, goldenRecipe())
	cap24, cap1200 := 24, 1200
	selected := SelectPairs(sim, SelectBudgets{PerProfileCap: &cap24, GlobalCap: &cap1200}, nil)

	// ---- score（batch=1，default.yaml budgets.n_profiles_to_score_together 覆盖为 1）----
	// instruction 含 "a_to_b"：fake 路由按此识别打分类 prompt
	// （spec/04-fixtures.md §7.1；Python 基线的默认打分模板同样含
	// 该输出格式标记）。
	scores, unscored := ScorePairs(
		selected,
		goldenSectionsDict(g),
		"score instruction: respond with a_to_b and b_to_a scores",
		"{user1_sections}\n{user2_sections}\n{instruction}",
		fakeLLM{},
		ScoreBudgets{PerProfileCap: &cap24, MaxCalls: &cap1200, BatchSize: 1},
	)
	if len(unscored) != len(g.UnscoredPairIDs) {
		t.Fatalf("unscored 数: got %d want %d", len(unscored), len(g.UnscoredPairIDs))
	}
	scores = PrepareNormalizedScores(scores, nil, nil)
	if len(scores.Order) != len(g.PairScoreOrder) {
		t.Fatalf("score order 长度: got %d want %d", len(scores.Order), len(g.PairScoreOrder))
	}
	for i, id := range scores.Order {
		if string(id) != g.PairScoreOrder[i] {
			t.Fatalf("score order[%d]: got %s want %s", i, id, g.PairScoreOrder[i])
		}
	}
	for id, want := range g.PairScores {
		got := scores.ByID[domain.PairID(id)].ToMap()
		assertJSONValueEqual(t, "pair_score "+id, got, want)
	}

	// ---- pre_matrix ----
	userIDs := make([]domain.UserID, len(b.UserIDs))
	copy(userIDs, b.UserIDs)
	pref := BuildPrefMatrix(scores, userIDs)
	assertMatrixEqual(t, "pref_left_to_right", pref.PrefLeftToRight, g.PrefMatrix.PrefLeftToRight)
	assertMatrixEqual(t, "pref_right_to_left", pref.PrefRightToLeft, g.PrefMatrix.PrefRightToLeft)

	// ---- match（default.yaml matching/blending）----
	poolBMax := 0 // default: null → 不限；nil 语义
	_ = poolBMax
	outcome := SolveMatch(pref,
		MatchingConfig{BMin: 3, BMax: 4},
		BlendingConfig{EmbedWeight: 0.35, LLMWeight: 0.65},
	)
	if len(outcome.Edges) != len(g.Match.Edges) {
		t.Fatalf("edges 数: got %d want %d", len(outcome.Edges), len(g.Match.Edges))
	}
	for i, e := range outcome.Edges {
		assertJSONValueEqual(t, "edge", e.ToMap(), g.Match.Edges[i])
	}
	assertMatrixEqual(t, "match_prob", outcome.MatchProb, g.Match.MatchProb)

	// envy 报告：计数与 b_min 字段。
	envy := outcome.EnvyReport
	assertJSONValueEqual(t, "envy_report", envy, g.Match.EnvyReport)
	if envy["b_min_satisfied"].(bool) != g.Match.EnvyReport["b_min_satisfied"].(bool) {
		t.Errorf("b_min_satisfied: got %v want %v", envy["b_min_satisfied"], g.Match.EnvyReport["b_min_satisfied"])
	}

	// ---- report ----
	report := CreateReport(outcome.Edges, goldenExtracted(g), 0, nil)
	assertJSONValueEqual(t, "report", report, g.Report)
}

// TestGoldenScoreBatchFlow 批量打分路径（batch=2，CodeRabbit）：替身按
// "### Pair N" 分块返回 JSON 数组，整批打分成功且分数与 batch=1 的
// golden 期望逐对一致——批量路径不再只靠 BatchSize==1 覆盖。
func TestGoldenScoreBatchFlow(t *testing.T) {
	g := loadGoldenFlow(t)
	b := goldenBundle(t, g)
	sim := ComputeSimilarity(b, nil, goldenRecipe())
	cap24, cap1200 := 24, 1200
	selected := SelectPairs(sim, SelectBudgets{PerProfileCap: &cap24, GlobalCap: &cap1200}, nil)

	scores, unscored := ScorePairs(
		selected,
		goldenSectionsDict(g),
		"score instruction: respond with a_to_b and b_to_a scores",
		"{user1_sections}\n{user2_sections}\n{instruction}",
		fakeLLM{},
		ScoreBudgets{PerProfileCap: &cap24, MaxCalls: &cap1200, BatchSize: 2},
	)
	if len(unscored) != len(g.UnscoredPairIDs) {
		t.Fatalf("batch=2 unscored 数: got %d want %d（批量响应未被数组契约接受）",
			len(unscored), len(g.UnscoredPairIDs))
	}
	scores = PrepareNormalizedScores(scores, nil, nil)
	// 打分集合与 batch=1 golden 一致（分数按 pair_id 查表，与批次无关）。
	if len(scores.Order) != len(g.PairScoreOrder) {
		t.Fatalf("batch=2 score order 长度: got %d want %d", len(scores.Order), len(g.PairScoreOrder))
	}
	for id, want := range g.PairScores {
		got := scores.ByID[domain.PairID(id)].ToMap()
		assertJSONValueEqual(t, "batch pair_score "+id, got, want)
	}
}

// goldenExtracted 重建 report 用的 ExtractedSections（id 全集）。
func goldenExtracted(g *goldenFlow) []domain.ExtractedSections {
	out := make([]domain.ExtractedSections, 0, len(g.Bundle.UserIDs))
	for _, uid := range g.Bundle.UserIDs {
		sections := map[domain.SectionName]string{}
		for _, name := range g.Bundle.SectionNames {
			sections[domain.SectionName(name)] = uid + " " + name
		}
		out = append(out, domain.NewExtractedSections(domain.UserID(uid), sections, ""))
	}
	return out
}

// assertJSONValueEqual 递归比较 JSON 反序列化后的结构
// （map[string]any / []any / 标量；float 按 bit 相等）。
func assertJSONValueEqual(t *testing.T, path string, got, want any) {
	t.Helper()
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			// Go 侧 map[string]int（如 degree_distribution）→ 转成
			// map[string]any 再比（int 走 asFloat 数值比较）。
			if mi, ok2 := got.(map[string]int); ok2 {
				g = make(map[string]any, len(mi))
				for k, v := range mi {
					g[k] = v
				}
			} else {
				t.Errorf("%s: got %T want map", path, got)
				return
			}
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok {
				t.Errorf("%s.%s: 缺失键（got keys %v）", path, k, keysOf(g))
				continue
			}
			assertJSONValueEqual(t, path+"."+k, gv, wv)
		}
		for k := range g {
			if _, ok := w[k]; !ok {
				t.Errorf("%s.%s: 多余键", path, k)
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			// Go 侧 []string（如 b_min_violations）→ 转成 []any 再比。
			if ss, ok2 := got.([]string); ok2 {
				g = make([]any, len(ss))
				for k, s := range ss {
					g[k] = s
				}
			} else {
				t.Errorf("%s: got %T want slice", path, got)
				return
			}
		}
		if len(g) != len(w) {
			t.Errorf("%s: 长度 got %d want %d", path, len(g), len(w))
			return
		}
		for i := range w {
			assertJSONValueEqual(t, path+"[]", g[i], w[i])
		}
	case float64:
		gf, ok := asFloat(got)
		if !ok || gf != w {
			t.Errorf("%s: got %v(%T) want %v", path, got, got, w)
		}
	case nil:
		if got != nil {
			t.Errorf("%s: got %v want null", path, got)
		}
	case bool, string:
		if got != want {
			t.Errorf("%s: got %v want %v", path, got, want)
		}
	default:
		// JSON 反序列化只产出 float64；Go 侧若是 int 系，统一按数值比。
		gf, ok := asFloat(got)
		wf, ok2 := asFloat(want)
		if !ok || !ok2 || gf != wf {
			t.Errorf("%s: got %v(%T) want %v(%T)", path, got, got, want, want)
		}
	}
}

// asFloat 把 JSON 标量（Go int / int64 / float64）统一成 float64。
func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	default:
		return 0, false
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
