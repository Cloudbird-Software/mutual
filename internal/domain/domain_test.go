package domain

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"testing"
)

// golden/domain/hash_vectors.json 由 Python 基线生成（scripts/ 内的
// 一次性脚本），证明 Go 侧 hash 链路与 Python 逐字节一致：
// sections → json.dumps(sort_keys=True) → sha256[:16]。
func TestHashVectorsGolden(t *testing.T) {
	raw, err := os.ReadFile("../../golden/domain/hash_vectors.json")
	if err != nil {
		t.Fatalf("读取 golden hash 向量失败: %v", err)
	}
	var doc struct {
		HashVectors []struct {
			Sections map[string]string `json:"sections"`
			Dump     string            `json:"dump"`
			Hash     string            `json:"hash"`
		} `json:"hash_vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 golden hash 向量失败: %v", err)
	}
	if len(doc.HashVectors) < 8 {
		t.Fatalf("golden hash 向量过少: %d", len(doc.HashVectors))
	}
	for i, v := range doc.HashVectors {
		sections := make(map[SectionName]string, len(v.Sections))
		for k, s := range v.Sections {
			sections[SectionName(k)] = s
		}
		if got := pyJSONDumpSections(map[string]string(v.Sections)); got != v.Dump {
			t.Errorf("第 %d 条 json dump: got %q want %q", i, got, v.Dump)
		}
		es := NewExtractedSections("u", sections, "")
		if es.Hash != v.Hash {
			t.Errorf("第 %d 条 hash: got %s want %s", i, es.Hash, v.Hash)
		}
	}
}

// pyRound 必须复刻 Python round(x, 3)（含 banker's rounding 与
// 二进制表示效应，如 0.1235 → 0.123、0.0625 → 0.062）。
func TestPyRoundGolden(t *testing.T) {
	raw, err := os.ReadFile("../../golden/domain/hash_vectors.json")
	if err != nil {
		t.Fatalf("读取 golden round 向量失败: %v", err)
	}
	var doc struct {
		RoundVectors []struct {
			X string `json:"x"`
			N int    `json:"n"`
			R string `json:"r"`
		} `json:"round_vectors"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("解析 golden round 向量失败: %v", err)
	}
	for i, v := range doc.RoundVectors {
		x, err1 := strconv.ParseFloat(v.X, 64)
		want, err2 := strconv.ParseFloat(v.R, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("第 %d 条解析失败: %v / %v", i, err1, err2)
		}
		if got := PyRound(x, v.N); math.Float64bits(got) != math.Float64bits(want) {
			t.Errorf("第 %d 条 round(%v, %d): got %v want %v", i, x, v.N, got, want)
		}
	}
}

func TestStablePairIDOrderIndependent(t *testing.T) {
	ab := StablePairID("alice", "bob")
	ba := StablePairID("bob", "alice")
	if ab != ba {
		t.Fatalf("pair id 与顺序相关: %q vs %q", ab, ba)
	}
	if ab != "alice__bob" {
		t.Fatalf("pair id 归一化错误: %q", ab)
	}
}

func TestNewCandidatePairNormalizes(t *testing.T) {
	p := NewCandidatePair("zoe", "alice", 0.5)
	if p.User1 != "alice" || p.User2 != "zoe" {
		t.Fatalf("候选对未归一化: %v → %v", "zoe/alice", p.User1+"/"+p.User2)
	}
	if p.PairID != "alice__zoe" {
		t.Fatalf("pair id 错误: %q", p.PairID)
	}
}

func TestPairScoreToMapOmitsNilNormalized(t *testing.T) {
	ps := NewPairScore("b", "a", 0.12345678)
	m := ps.ToMap()
	if _, ok := m["embed_score_normalized"]; ok {
		t.Fatalf("nil normalized 字段应省略键")
	}
	if m["embed_score"] != 0.123 { // 0.12345678 → round 3
		t.Fatalf("embed_score 舍入错误: %v", m["embed_score"])
	}
	if m["llm_score"] != nil {
		t.Fatalf("未打分 llm_score 应为 null")
	}
}

func TestBundleSubsetUnknownUser(t *testing.T) {
	// 构造形状自洽的 bundle：2 用户 × 1 分节 × 2 维。
	b := &EmbeddingsBundle{
		UserIDs:      []UserID{"a", "b"},
		SectionNames: []SectionName{"s"},
		Dim:          2,
		Embeddings: EmbeddingTensor{
			{{{1, 2}}},
			{{{3, 4}}},
		},
		Hyde: map[SectionName][][]Vector{
			"s": {{{5, 6}}, {{7, 8}}},
		},
	}
	if _, err := b.Subset([]UserID{"a", "c"}); err == nil {
		t.Fatalf("未知用户应报契约错误")
	}
	sub, err := b.Subset([]UserID{"b", "a"})
	if err != nil {
		t.Fatalf("合法子集失败: %v", err)
	}
	if sub.UserIDs[0] != "b" || sub.UserIDs[1] != "a" {
		t.Fatalf("子集顺序错误: %v", sub.UserIDs)
	}
	if sub.Embeddings[0][0][0][0] != 3 || sub.Embeddings[1][0][0][0] != 1 {
		t.Fatalf("子集张量取值错误")
	}
	if sub.Hyde["s"][0][0][0] != 7 || sub.Hyde["s"][1][0][0] != 5 {
		t.Fatalf("子集 hyde 取值错误")
	}
}

func TestEvaluationGates(t *testing.T) {
	r := EvaluationReport{HRAt3: 0.65, NDCGAt5: 0.45}
	if !r.PassesGates(nil) {
		t.Fatalf("默认门禁应通过")
	}
	r.HRAt3 = 0.55
	if r.PassesGates(nil) {
		t.Fatalf("HR@3 低于门禁应失败")
	}
	g := Gates{HRAt3Min: 0.5, NDCGAt5Min: 0.4, TotalEnvyMax: 0}
	r.HRAt3 = 0.55
	r.EnvyCountLeft = 1
	if r.PassesGates(&g) {
		t.Fatalf("envy 超限应失败")
	}
	r.EnvyCountLeft = 0
	if !r.PassesGates(&g) {
		t.Fatalf("自定义门禁应通过")
	}
}

func TestGatesFromMapDefaults(t *testing.T) {
	g := GatesFromMap(map[string]any{})
	d := DefaultGates()
	if g != d {
		t.Fatalf("空配置应回落默认门禁: %+v vs %+v", g, d)
	}
	g = GatesFromMap(map[string]any{"hr_at_3_min": 0.7, "total_envy_max": 5})
	if g.HRAt3Min != 0.7 || g.TotalEnvyMax != 5 || g.NDCGAt5Min != 0.4 {
		t.Fatalf("GatesFromMap 解析错误: %+v", g)
	}
}

func TestProfileFromMapContract(t *testing.T) {
	if _, err := ProfileFromMap(map[string]any{}); err == nil {
		t.Fatalf("缺 id 应报契约错误")
	}
	p, err := ProfileFromMap(map[string]any{
		"id":       "alice",
		"sections": map[string]any{"skills": "AI"},
	})
	if err != nil {
		t.Fatalf("合法 profile 解析失败: %v", err)
	}
	if p.ID != "alice" || p.Sections["skills"] != "AI" || p.LastUpdatedAt != nil {
		t.Fatalf("profile 解析错误: %+v", p)
	}
}

// TestEnvyRate 规模化公平性度量（total_envy O(N²) 失义的归一化）。
func TestEnvyRate(t *testing.T) {
	r := EvaluationReport{EnvyCountLeft: 4, EnvyCountRight: 6, TotalScenarios: 20}
	if got := r.EnvyRate(); got != 0.5 {
		t.Fatalf("EnvyRate: got %v want 0.5", got)
	}
	zero := EvaluationReport{}
	if got := zero.EnvyRate(); got != 0 {
		t.Fatalf("零场景 EnvyRate: got %v want 0", got)
	}
}
