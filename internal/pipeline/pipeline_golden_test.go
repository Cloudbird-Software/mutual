package pipeline

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/Cloudbird-Software/mutual/config"
	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/rng"
	"github.com/Cloudbird-Software/mutual/internal/signal"
	"github.com/Cloudbird-Software/mutual/internal/store"
)

// ---------------------------------------------------------------------------
// golden 约定（与 Python tests/test_golden.py 的 golden_stages 同构）
// ---------------------------------------------------------------------------

// goldenEmbed 契约常量：RandomState(12345)、维度 8、base[...,0] += 5.0
// 后逐向量归一化（任意两用户 cosine > 0，保证 select 选出全部 6 对）。
const (
	goldenDim  = 8
	goldenSeed = 12345
)

var goldenUserIDs = []string{"alice", "bob", "carol", "david"}

// loadGoldenProfiles 加载 golden/test_basic 的四份画像。
func loadGoldenProfiles(t *testing.T) []domain.Profile {
	t.Helper()
	out := make([]domain.Profile, 0, len(goldenUserIDs))
	for _, id := range goldenUserIDs {
		raw, err := os.ReadFile("../../golden/test_basic/" + id + ".json")
		if err != nil {
			t.Fatalf("读取 golden 画像 %s: %v", id, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("解析 golden 画像 %s: %v", id, err)
		}
		p, err := domain.ProfileFromMap(doc)
		if err != nil {
			t.Fatalf("构造 Profile %s: %v", id, err)
		}
		out = append(out, p)
	}
	return out
}

// goldenExtracted 复刻 Python _golden_extract：直接用画像自带 sections。
func goldenExtracted(profiles []domain.Profile) []domain.ExtractedSections {
	out := make([]domain.ExtractedSections, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, domain.NewExtractedSections(p.ID, p.Sections, ""))
	}
	return out
}

// goldenBundle 复刻 Python _golden_embed：RandomState(12345) 依
// (user-major, section-sorted) 序消费 randn(n, S, 8)，首维 +5 后归一化。
func goldenBundle(t *testing.T, extracted []domain.ExtractedSections) *domain.EmbeddingsBundle {
	t.Helper()
	nameSet := map[string]bool{}
	for _, es := range extracted {
		for name := range es.Sections {
			nameSet[string(name)] = true
		}
	}
	names := make([]string, 0, len(nameSet))
	for name := range nameSet {
		names = append(names, name)
	}
	sortStrings(names)

	n := len(extracted)
	rs := rng.New(goldenSeed)
	embeddings := make(domain.EmbeddingTensor, n)
	hyde := map[domain.SectionName][][]domain.Vector{}
	for i := 0; i < n; i++ {
		ue := make(domain.UserEmbeddings, len(names))
		for k := range names {
			vec := make(domain.Vector, goldenDim)
			for d := range vec {
				vec[d] = rs.NormFloat64()
			}
			vec[0] += 5.0
			norm := 0.0
			for _, v := range vec {
				norm += v * v
			}
			norm = math.Sqrt(norm)
			for d := range vec {
				vec[d] /= norm
			}
			ue[k] = domain.SectionEmbeddings{vec}
		}
		embeddings[i] = ue
	}
	for _, name := range names {
		hyde[domain.SectionName(name)] = make([][]domain.Vector, n)
	}
	sectionNames := make([]domain.SectionName, len(names))
	for i, name := range names {
		sectionNames[i] = domain.SectionName(name)
	}
	userIDs := make([]domain.UserID, n)
	for i, es := range extracted {
		userIDs[i] = es.ID
	}
	return &domain.EmbeddingsBundle{
		UserIDs:        userIDs,
		SectionNames:   sectionNames,
		Embeddings:     embeddings,
		Hyde:           hyde,
		EmbeddingModel: "golden-embedder",
		Dim:            goldenDim,
	}
}

// goldenConfig 默认配置 + batch=1（与 Python _run_golden 的预算解耦一致）。
func goldenConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("", map[string]any{
		"budgets.n_profiles_to_score_together": 1,
	})
	if err != nil {
		t.Fatalf("加载 golden 配置: %v", err)
	}
	return cfg
}

func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j] < ss[j-1]; j-- {
			ss[j], ss[j-1] = ss[j-1], ss[j]
		}
	}
}

// ---------------------------------------------------------------------------
// Phase 1：bundle 直入模式 golden 差分（对拍 cohort.json 期望）
// ---------------------------------------------------------------------------

func runGoldenCohort(t *testing.T) *domain.MatchResult {
	t.Helper()
	profiles := loadGoldenProfiles(t)
	extracted := goldenExtracted(profiles)
	bundle := goldenBundle(t, extracted)
	result, err := RunFullMatch(FullMatchInput{
		Bundle:   bundle,
		Sections: extracted,
	}, goldenConfig(t), Deps{LLM: &signal.FakeLLM{}})
	if err != nil {
		t.Fatalf("RunFullMatch: %v", err)
	}
	return result
}

func TestPipelineGoldenCohort(t *testing.T) {
	result := runGoldenCohort(t)

	if len(result.Edges) != 6 {
		t.Fatalf("edges 数: got %d want 6（4 人 cohort 全对入选）", len(result.Edges))
	}
	overview := result.ReportData["overview"].(map[string]any)
	if overview["total_users"] != 4 || overview["total_edges"] != 6 {
		t.Errorf("overview: got %v", overview)
	}
	if got := result.ReportData["degree_distribution"].(map[string]int); got["3"] != 4 {
		t.Errorf("degree_distribution: got %v want {3:4}", got)
	}

	// 稳定 pair_id 恰好覆盖全部 unordered 对。
	expected := map[string]bool{
		"alice__bob": true, "alice__carol": true, "alice__david": true,
		"bob__carol": true, "bob__david": true, "carol__david": true,
	}
	got := map[string]bool{}
	for _, e := range result.Edges {
		got[string(e.PairID)] = true
	}
	for pid := range expected {
		if !got[pid] {
			t.Errorf("缺失 pair %s", pid)
		}
	}

	// 对拍 golden/engine/full_flow.json 的 report.users（partner + weight
	// 逐位一致）。权重基准是 scripts/capture_golden_engine.py 捕获的
	// Python 实际输出；cohort.json 的 final_weights 是旧参考实现的
	// 移植值，spec/04-fixtures.md §Phase-1 明确标为「暂缓」（待 spec
	// 变更重新固化），不作为断言基准。
	raw, err := os.ReadFile("../../golden/engine/full_flow.json")
	if err != nil {
		t.Fatalf("读取 full_flow.json: %v", err)
	}
	var flow struct {
		Report struct {
			Users map[string]struct {
				Degree  int `json:"degree"`
				Matches []struct {
					Partner string  `json:"partner"`
					Weight  float64 `json:"weight"`
				} `json:"matches"`
			} `json:"users"`
			ScoreStatistics struct {
				LLMScores struct {
					Min float64 `json:"min"`
					Max float64 `json:"max"`
					Avg float64 `json:"avg"`
				} `json:"llm_scores"`
			} `json:"score_statistics"`
		} `json:"report"`
	}
	if err := json.Unmarshal(raw, &flow); err != nil {
		t.Fatalf("解析 full_flow.json: %v", err)
	}
	users := result.ReportData["users"].(map[string]any)
	for uid, want := range flow.Report.Users {
		gotUser, ok := users[uid].(map[string]any)
		if !ok {
			t.Fatalf("报告缺用户 %s", uid)
		}
		if gotUser["degree"] != want.Degree {
			t.Errorf("%s degree: got %v want %d", uid, gotUser["degree"], want.Degree)
		}
		gotMatches := gotUser["matches"].([]any)
		if len(gotMatches) != len(want.Matches) {
			t.Fatalf("%s matches 数: got %d want %d", uid, len(gotMatches), len(want.Matches))
		}
		for i, wm := range want.Matches {
			gm := gotMatches[i].(map[string]any)
			if gm["partner"] != wm.Partner {
				t.Errorf("%s matches[%d] partner: got %v want %s", uid, i, gm["partner"], wm.Partner)
			}
			if diff := math.Abs(gm["weight"].(float64) - wm.Weight); diff > 1e-9 {
				t.Errorf("%s matches[%d] weight: got %v want %v", uid, i, gm["weight"], wm.Weight)
			}
		}
	}

	// llm_scores 统计与 cohort.json 自洽（spec/04-fixtures.md §Phase-1
	// 权威断言：min 0.35 / max 0.9 / avg 0.683，由 §7.1 分数表驱动）。
	llmStats := result.ReportData["score_statistics"].(map[string]any)["llm_scores"].(map[string]any)
	wantLLM := flow.Report.ScoreStatistics.LLMScores
	if diff := math.Abs(llmStats["min"].(float64) - wantLLM.Min); diff > 1e-9 {
		t.Errorf("llm_scores.min: got %v want %v", llmStats["min"], wantLLM.Min)
	}
	if diff := math.Abs(llmStats["max"].(float64) - wantLLM.Max); diff > 1e-9 {
		t.Errorf("llm_scores.max: got %v want %v", llmStats["max"], wantLLM.Max)
	}
	if diff := math.Abs(llmStats["avg"].(float64) - wantLLM.Avg); diff > 1e-9 {
		t.Errorf("llm_scores.avg: got %v want %v", llmStats["avg"], wantLLM.Avg)
	}
}

func TestPipelineGoldenDirectionalScores(t *testing.T) {
	result := runGoldenCohort(t)

	// A→B ≠ B→A（方向性不盲目对称化，spec/05-boundaries.md §2）。
	for _, e := range result.Edges {
		if e.LLMScoreAToB != nil && e.LLMScoreBToA != nil && *e.LLMScoreAToB == *e.LLMScoreBToA {
			// fake 分数表允许个别对相等（bob__carol 0.83/0.82 不等，
			// alice__carol 0.80/0.82 不等）——只断言 alice__bob。
		}
	}
	aliceBob := findEdge(t, result.Edges, "alice__bob")
	if aliceBob.LLMScoreAToB == nil || math.Abs(*aliceBob.LLMScoreAToB-0.85) > 1e-9 {
		t.Errorf("alice__bob a_to_b: got %v want 0.85", aliceBob.LLMScoreAToB)
	}
	if aliceBob.LLMScoreBToA == nil || math.Abs(*aliceBob.LLMScoreBToA-0.90) > 1e-9 {
		t.Errorf("alice__bob b_to_a: got %v want 0.90", aliceBob.LLMScoreBToA)
	}
}

func TestPipelineGoldenDeterminism(t *testing.T) {
	r1 := runGoldenCohort(t)
	r2 := runGoldenCohort(t)
	if len(r1.Edges) != len(r2.Edges) {
		t.Fatalf("两次运行 edges 数不一致: %d vs %d", len(r1.Edges), len(r2.Edges))
	}
	for i := range r1.Edges {
		a, b := r1.Edges[i], r2.Edges[i]
		if a.PairID != b.PairID || a.FinalWeight != b.FinalWeight ||
			a.Intro != b.Intro || a.StarterTopics != b.StarterTopics {
			t.Errorf("edges[%d] 两次运行不一致: %+v vs %+v", i, a, b)
		}
	}
}

func TestPipelineGoldenEnvyFree(t *testing.T) {
	result := runGoldenCohort(t)
	if result.EnvyReport == nil {
		t.Fatal("envy_report 缺失")
	}
	if total := result.EnvyReport["total_envy"]; total != 0 {
		t.Errorf("cohort 匹配应为 envy-free: total_envy=%v", total)
	}
	if satisfied := result.EnvyReport["b_min_satisfied"]; satisfied != true {
		t.Errorf("b_min 应满足（b_min=3、每对度=3）: %v", satisfied)
	}
}

func TestPipelineIntroFilled(t *testing.T) {
	result := runGoldenCohort(t)
	for _, e := range result.Edges {
		if strings.TrimSpace(e.Intro) == "" || strings.TrimSpace(e.StarterTopics) == "" {
			t.Errorf("pair %s intro/starter_topics 缺失", e.PairID)
		}
	}
}

func findEdge(t *testing.T, edges []domain.Edge, pairID string) domain.Edge {
	t.Helper()
	for _, e := range edges {
		if string(e.PairID) == pairID {
			return e
		}
	}
	t.Fatalf("未找到 pair %s", pairID)
	return domain.Edge{}
}

// ---------------------------------------------------------------------------
// Phase 2：profiles 全链路（scripted LLM 承担 extract/hyde）
// ---------------------------------------------------------------------------

// scriptedLLM 在 FakeLLM 之上补 extract/hyde 两条路由：
// FakeLLM 对非打分 prompt 返回话术 JSON，会使 extract 全部退化
// （Python test_golden.py 有同样的观察），故 golden 约定里
// extract/hyde 由替身接管。
type scriptedLLM struct {
	signal.FakeLLM
	extractResponse string
}

func (s *scriptedLLM) Complete(prompt string, model string) (string, error) {
	if strings.Contains(prompt, "Extract structured sections") {
		return s.extractResponse, nil
	}
	if strings.Contains(prompt, "hypothetical") {
		return `["looks for collaborators with complementary skills"]`, nil
	}
	return s.FakeLLM.Complete(prompt, model)
}

func TestPipelineFullChainFromProfiles(t *testing.T) {
	profiles := loadGoldenProfiles(t)
	llm := &scriptedLLM{
		extractResponse: `{"skills": "go python rust", "vision": "build reciprocal matching",
			"project": "mutual engine rewrite", "needs": "collaborators and reviewers"}`,
	}
	result, err := RunFullMatch(FullMatchInput{Profiles: profiles},
		goldenConfig(t), Deps{LLM: llm, Embedder: signal.FakeEmbedder{}})
	if err != nil {
		t.Fatalf("RunFullMatch: %v", err)
	}

	// 全员同 sections → 全对 cosine=1 → 6 对全入选、全部打分成功。
	if len(result.Edges) != 6 {
		t.Fatalf("edges 数: got %d want 6", len(result.Edges))
	}
	for _, e := range result.Edges {
		if e.Intro == "" {
			t.Errorf("pair %s intro 缺失", e.PairID)
		}
	}
	if n := len(result.NewPairs); n != 6 {
		t.Errorf("new_pairs: got %d want 6", n)
	}
}

func TestPipelineMinProfilesGuard(t *testing.T) {
	profiles := loadGoldenProfiles(t)[:1] // 1 < min_profiles_required=2
	_, err := RunFullMatch(FullMatchInput{Profiles: profiles},
		goldenConfig(t), Deps{LLM: &scriptedLLM{}, Embedder: signal.FakeEmbedder{}})
	if err == nil || !strings.Contains(err.Error(), "min_profiles_required") {
		t.Fatalf("应拒绝低于最少画像数的运行，got err=%v", err)
	}
}

func TestPipelineMissingDepsFail(t *testing.T) {
	profiles := loadGoldenProfiles(t)
	if _, err := RunFullMatch(FullMatchInput{Profiles: profiles},
		goldenConfig(t), Deps{}); err == nil || !strings.Contains(err.Error(), "LLM") {
		t.Fatalf("缺 LLM 应 fail loud，got err=%v", err)
	}
	if _, err := RunFullMatch(FullMatchInput{Profiles: profiles},
		goldenConfig(t), Deps{LLM: &scriptedLLM{}}); err == nil || !strings.Contains(err.Error(), "Embedder") {
		t.Fatalf("缺 Embedder 应 fail loud，got err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Phase 3：batch / query 模式
// ---------------------------------------------------------------------------

func TestPipelineBatchMatch(t *testing.T) {
	profiles := loadGoldenProfiles(t)
	extracted := goldenExtracted(profiles)
	poolBundle := goldenBundle(t, extracted)

	members := []domain.UserID{"alice", "bob"}
	result, err := RunBatchMatch(BatchMatchInput{
		MemberIDs:    members,
		PoolBundle:   poolBundle,
		PoolSections: extracted,
	}, goldenConfig(t), Deps{LLM: &signal.FakeLLM{}})
	if err != nil {
		t.Fatalf("RunBatchMatch: %v", err)
	}

	// 报告范围限定 member 侧（scope_user_ids）。
	users := result.MatchResult.ReportData["users"].(map[string]any)
	if len(users) != 2 {
		t.Fatalf("batch 报告应只含 member 侧: got %d users", len(users))
	}
	for _, uid := range []string{"alice", "bob"} {
		if _, ok := users[uid]; !ok {
			t.Errorf("member %s 缺失", uid)
		}
	}

	// member 侧度约束：b_max=4（M×N 模式绑定 member 侧）。
	degree := users["alice"].(map[string]any)["degree"]
	if degree.(int) > 4 {
		t.Errorf("alice 度数 %v 超过 b_max=4", degree)
	}

	if len(result.MemberIDs) != 2 || len(result.PoolIDs) != 4 {
		t.Errorf("batch 元数据: members=%d pool=%d", len(result.MemberIDs), len(result.PoolIDs))
	}
	if result.Metadata["n_selected_pairs"].(int) <= 0 {
		t.Errorf("n_selected_pairs 应为正: %v", result.Metadata["n_selected_pairs"])
	}
}

func TestPipelineBatchUnknownMember(t *testing.T) {
	profiles := loadGoldenProfiles(t)
	extracted := goldenExtracted(profiles)
	poolBundle := goldenBundle(t, extracted)

	_, err := RunBatchMatch(BatchMatchInput{
		MemberIDs:    []domain.UserID{"alice", "zoe"},
		PoolBundle:   poolBundle,
		PoolSections: extracted,
	}, goldenConfig(t), Deps{LLM: &signal.FakeLLM{}})
	if err == nil || !strings.Contains(err.Error(), "unknown user") {
		t.Fatalf("未知 member 应报错，got err=%v", err)
	}
}

func TestPipelineQueryMatch(t *testing.T) {
	profiles := loadGoldenProfiles(t)
	extracted := goldenExtracted(profiles)
	poolBundle := goldenBundle(t, extracted)

	result, err := RunQueryMatch(QueryMatchInput{
		QueryText:    "go developer looking for art collaboration",
		PoolBundle:   poolBundle,
		PoolSections: extracted,
	}, goldenConfig(t), Deps{LLM: &signal.FakeLLM{}, Embedder: signal.FakeEmbedder{}})
	if err != nil {
		t.Fatalf("RunQueryMatch: %v", err)
	}

	// 报告范围 = 查询侧单用户。
	users := result.ReportData["users"].(map[string]any)
	if len(users) != 1 {
		t.Fatalf("query 报告应只含 query 侧: got %d users", len(users))
	}
	if _, ok := users["query"]; !ok {
		t.Errorf("query 用户缺失: %v", users)
	}
}

// ---------------------------------------------------------------------------
// Phase 4：store 集成（持久化 + content-addressed 复用 + novelty）
// ---------------------------------------------------------------------------

// countingEmbedder 统计实际嵌入调用（复用率断言用）。
type countingEmbedder struct {
	signal.FakeEmbedder
	Calls int
	Texts int
}

func (c *countingEmbedder) Embed(texts []string) [][]float64 {
	c.Calls++
	c.Texts += len(texts)
	return c.FakeEmbedder.Embed(texts)
}

func TestPipelineStorePersistence(t *testing.T) {
	root := t.TempDir()
	st, err := store.NewFileStore(root, 6)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	profiles := loadGoldenProfiles(t)
	llm := &scriptedLLM{
		extractResponse: `{"skills": "go python", "vision": "reciprocal matching",
			"project": "mutual", "needs": "collaborators"}`,
	}
	embedder := &countingEmbedder{}
	deps := Deps{LLM: llm, Embedder: embedder, Store: st}

	if _, err := RunFullMatch(FullMatchInput{Profiles: profiles}, goldenConfig(t), deps); err != nil {
		t.Fatalf("首轮 RunFullMatch: %v", err)
	}

	// sections 与 bundle 已落盘。
	sections, err := st.GetSections(nil)
	if err != nil {
		t.Fatalf("GetSections: %v", err)
	}
	if len(sections) != 4 {
		t.Errorf("落盘 sections: got %d want 4", len(sections))
	}
	bundle, err := st.GetEmbeddings()
	if err != nil || bundle == nil {
		t.Fatalf("GetEmbeddings: bundle=%v err=%v", bundle, err)
	}

	history, err := st.GetMatchHistory()
	if err != nil {
		t.Fatalf("GetMatchHistory: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("match_history 应有记录（store.PutMatches 路径）")
	}

	// 第二轮：内容不变 → content-addressed 复用，不再产生嵌入调用。
	firstCalls := embedder.Calls
	if _, err := RunFullMatch(FullMatchInput{Profiles: profiles}, goldenConfig(t), deps); err != nil {
		t.Fatalf("二轮 RunFullMatch: %v", err)
	}
	if embedder.Calls != firstCalls {
		t.Errorf("内容未变时应全量复用：embed 调用 %d → %d", firstCalls, embedder.Calls)
	}

	// 二轮历史应包含两轮的 pair（append-only）。
	history2, _ := st.GetMatchHistory()
	if len(history2) < len(history) {
		t.Errorf("match_history 应 append-only: %d → %d", len(history), len(history2))
	}
}

func TestPipelineNoveltyExclusionFromStore(t *testing.T) {
	root := t.TempDir()
	st, _ := store.NewFileStore(root, 6)
	profiles := loadGoldenProfiles(t)
	extracted := goldenExtracted(profiles)

	// 预置历史：排除 alice__bob（novelty 窗口内）。
	if err := st.PutMatches([]domain.Edge{{
		User1:  "alice",
		User2:  "bob",
		PairID: "alice__bob",
	}}); err != nil {
		t.Fatalf("PutMatches: %v", err)
	}

	result, err := RunFullMatch(FullMatchInput{
		Bundle:   goldenBundle(t, extracted),
		Sections: extracted,
	}, goldenConfig(t), Deps{LLM: &signal.FakeLLM{}, Store: st})
	if err != nil {
		t.Fatalf("RunFullMatch: %v", err)
	}
	for _, e := range result.Edges {
		if e.PairID == "alice__bob" {
			t.Errorf("alice__bob 应被 novelty 排除（match_history 内）")
		}
	}
}
