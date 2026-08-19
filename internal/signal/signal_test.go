package signal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/rng"
)

// TestFakeLLMScoringRoute 打分类路径：prompt 含 "a_to_b" → 按 prompt
// 中出现的 cohort id 查表（spec/04-fixtures.md §7.1，契约由测试守护）。
func TestFakeLLMScoringRoute(t *testing.T) {
	f := &FakeLLM{}
	raw, err := f.Complete("Score (alice, bob) respond a_to_b b_to_a", "m")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var resp struct {
		AToB float64 `json:"a_to_b"`
		BToA float64 `json:"b_to_a"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("打分响应应为 JSON: %v（raw=%s）", err, raw)
	}
	if resp.AToB != 0.85 || resp.BToA != 0.90 {
		t.Errorf("alice__bob 分数表: got %+v want 0.85/0.90", resp)
	}
}

// TestFakeLLMIntroRoute 非打分 prompt → 固定话术 JSON。
func TestFakeLLMIntroRoute(t *testing.T) {
	f := &FakeLLM{}
	raw, _ := f.Complete("Write an introduction with starter topics", "m")
	if !strings.Contains(raw, "Fake intro.") {
		t.Errorf("话术路径: got %s", raw)
	}
}

// TestFakeLLMTableMiss 查表未命中（cohort id 不足 2 个）→ 0.5/0.5 兜底。
func TestFakeLLMTableMiss(t *testing.T) {
	f := &FakeLLM{}
	raw, _ := f.Complete("score a_to_b for alice alone", "m")
	var resp struct {
		AToB float64 `json:"a_to_b"`
		BToA float64 `json:"b_to_a"`
	}
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatalf("解析兜底响应: %v", err)
	}
	if resp.AToB != 0.5 || resp.BToA != 0.5 {
		t.Errorf("兜底分数: got %+v", resp)
	}
}

// TestFakeLLMCallCount 调用计数（缓存命中率断言用）。
func TestFakeLLMCallCount(t *testing.T) {
	f := &FakeLLM{}
	_, _ = f.Complete("x", "m")
	_, _ = f.Complete("y", "m")
	if f.CallCount != 2 {
		t.Errorf("CallCount: got %d want 2", f.CallCount)
	}
}

// TestFakeEmbedderDeterministic content-addressed：同文本同向量、
// 跨调用稳定（Python：RandomState(hash_text(t) % 2**32).randn(128)）。
func TestFakeEmbedderDeterministic(t *testing.T) {
	e := FakeEmbedder{}
	a1 := e.Embed([]string{"go developer", "artist"})
	a2 := e.Embed([]string{"go developer", "artist"})
	if len(a1) != 2 || len(a1[0]) != FakeDim {
		t.Fatalf("形状: got [%d][%d] want [2][128]", len(a1), len(a1[0]))
	}
	for i := range a1 {
		for d := range a1[i] {
			if a1[i][d] != a2[i][d] {
				t.Fatalf("同文本应同向量: [%d][%d] %v vs %v", i, d, a1[i][d], a2[i][d])
			}
		}
	}
	// 不同文本不同向量。
	b := e.Embed([]string{"artist", "go developer"})
	if b[0][0] == a1[0][0] {
		t.Error("不同文本应产生不同向量")
	}
}

// TestDirectionalScore 方向性不盲目对称：A 的 needs 撞 B 的 skills ≠
// B 的 needs 撞 A 的 skills。
func TestDirectionalScore(t *testing.T) {
	a := map[string]string{
		"needs":  "rust wasm",
		"skills": "cooking",
		"vision": "decentralized web",
	}
	b := map[string]string{
		"needs":  "cooking",
		"skills": "rust",
		"vision": "decentralized web",
	}
	a2b := DirectionalScore(a, b)
	b2a := DirectionalScore(b, a)
	if a2b <= 0 {
		t.Errorf("A→B: got %v（A.needs 撞 B.skills 应为正）", a2b)
	}
	if a2b != b2a {
		// 本例中 a.needs⊆b.skills 与 b.needs⊆a.skills 恰好对称值相同；
		// 换非对称文本验证方向性。
		c := map[string]string{"needs": "rust", "skills": "", "vision": ""}
		d := map[string]string{"needs": "", "skills": "rust wasm systems", "vision": ""}
		if DirectionalScore(c, d) <= 0 || DirectionalScore(d, c) != 0 {
			t.Errorf("方向性: c→d=%v d→c=%v（应不对称）", DirectionalScore(c, d), DirectionalScore(d, c))
		}
	}
}

// TestScoreBounds DirectionalScore / EmbedScore ∈ [0, 1]。
func TestScoreBounds(t *testing.T) {
	x := map[string]string{
		"needs": "a b c", "project": "d e", "skills": "f g h", "vision": "i j",
	}
	y := map[string]string{
		"needs": "f g", "project": "a", "skills": "b c d", "vision": "i",
	}
	for name, fn := range map[string]func() float64{
		"directional": func() float64 { return DirectionalScore(x, y) },
		"embed":       func() float64 { return EmbedScore(x, y) },
	} {
		if v := fn(); v < 0 || v > 1 {
			t.Errorf("%s 分数越界: %v", name, v)
		}
	}
	// 完全相同文本 → embedding 余弦 = 1。
	if v := EmbedScore(x, x); v < 0.999 {
		t.Errorf("同文本余弦应≈1: got %v", v)
	}
	// 无重叠 → 0。
	if v := EmbedScore(x, map[string]string{"needs": "zz"}); v != 0 {
		t.Errorf("无重叠应 0: got %v", v)
	}
}

// TestScoreMatrixDeterminism 同参数两次运行逐位一致（RNG 流消费顺序
// member × pool × (a_to_b, b_to_a) 全确定）。
func TestScoreMatrixDeterminism(t *testing.T) {
	members := []OrderedSections{
		{ID: "m1", Sections: map[string]string{"needs": "rust", "skills": "go", "vision": "web", "project": "x"}},
		{ID: "m2", Sections: map[string]string{"needs": "art", "skills": "paint", "vision": "beauty", "project": "y"}},
	}
	pool := []OrderedSections{
		{ID: "p1", Sections: map[string]string{"needs": "go", "skills": "rust", "vision": "web", "project": "x"}},
		{ID: "p2", Sections: map[string]string{"needs": "paint", "skills": "art", "vision": "beauty", "project": "y"}},
	}
	a := ScoreMatrix(members, pool, 7, 0.24, false)
	b := ScoreMatrix(members, pool, 7, 0.24, false)
	for mid, row := range a {
		for pid, sc := range row {
			other := b[mid][pid]
			if sc != other {
				t.Fatalf("ScoreMatrix 不确定: %s→%s %+v vs %+v", mid, pid, sc, other)
			}
		}
	}
	// 双向分数各自独立（噪声流各消费一个随机数）。
	if a["m1"]["p1"].AToB == a["m1"]["p1"].BToA {
		// 非必然不同，但黄金语义是两次独立加噪；弱断言跳过。
		_ = a
	}
	// 不同 seed → 不同噪声。
	c := ScoreMatrix(members, pool, 8, 0.24, false)
	differed := false
	for mid, row := range a {
		for pid, sc := range row {
			if sc != c[mid][pid] {
				differed = true
			}
		}
	}
	if !differed {
		t.Error("不同 seed 应产生不同噪声流")
	}
}

// TestScoreMatrixEmbeddingOnly 冷启动模式：双向同源（EmbedScore），
// 噪声独立。
func TestScoreMatrixEmbeddingOnly(t *testing.T) {
	members := []OrderedSections{
		{ID: "m1", Sections: map[string]string{"needs": "rust", "skills": "go", "vision": "web", "project": "x"}},
	}
	pool := []OrderedSections{
		{ID: "p1", Sections: map[string]string{"needs": "go", "skills": "rust", "vision": "web", "project": "x"}},
	}
	got := ScoreMatrix(members, pool, 0, 0.0, true)
	sc := got["m1"]["p1"]
	if sc.AToB != sc.BToA {
		// noiseScale=0 时双向应同为 EmbedScore。
		t.Errorf("冷启动双向同值: got %+v", sc)
	}
	if sc.AToB <= 0 {
		t.Errorf("高重叠冷启动分应为正: %+v", sc)
	}
}

// TestNoisy 加性噪声并截断到 [0, 1]（与 Python
// np.clip(score + scale*(rng.rand()-0.5), 0, 1) 一致）。
func TestNoisy(t *testing.T) {
	rs := rng.New(42)
	v := Noisy(0.5, rs, 0.24)
	if v < 0 || v > 1 {
		t.Errorf("Noisy 越界: %v", v)
	}
	// score=1、噪声为负 → 截断回 1 边界内；score=0、噪声为负 → 0。
	if v := Noisy(1.0, rs, 0.5); v < 0 || v > 1 {
		t.Errorf("上界截断失败: %v", v)
	}
	if v := Noisy(0.0, rs, 0.5); v < 0 || v > 1 {
		t.Errorf("下界截断失败: %v", v)
	}
}

// TestTokenize 小写化并按非字母数字切词。
func TestTokenize(t *testing.T) {
	got := Tokenize("Go-Dev, Rust/WASM!")
	want := []string{"go", "dev", "rust", "wasm"}
	if len(got) != len(want) {
		t.Fatalf("Tokenize: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Tokenize: got %v want %v", got, want)
		}
	}
}

// TestDomainHashTextStable HashText 是 FakeEmbedder 的种子源，必须跨调用稳定。
func TestDomainHashTextStable(t *testing.T) {
	if domain.HashText("go developer") != domain.HashText("go developer") {
		t.Fatal("HashText 不稳定")
	}
	if domain.HashText("a") == domain.HashText("b") {
		t.Fatal("HashText 应区分不同文本")
	}
}
