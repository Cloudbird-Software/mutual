// RT3 轮次画像分节长度上限（issue #50）的回归复现。
//
// 约定同 redteam_test.go：漏洞为真 → 修复后转绿常驻 CI。
package redteam

import (
	"strings"
	"testing"

	"github.com/Cloudbird-Software/mutual/config"
	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/pipeline"
	"github.com/Cloudbird-Software/mutual/internal/signal"
)

// TestRT3_SectionLengthFinancialDoS #50：超大分节/query 在注册层与
// pipeline 入口 fail-loud 拒绝——超大文本逐字进入该成员参与的每个
// LLM/embedding 调用（实测 2MB 分节渲染 2.96MB prompt）是面向运营方
// 的财务 DoS 面。
func TestRT3_SectionLengthFinancialDoS(t *testing.T) {
	huge := strings.Repeat("trade compliance logistics customs clearance bonded warehouse hainan fund ", 40000) // ~2MB

	// 注册层：ProfileFromMap 拒绝超长分节。
	if _, err := domain.ProfileFromMap(map[string]any{
		"id":       "dos_attacker",
		"sections": map[string]any{"skills": huge},
	}); err == nil {
		t.Fatal("REPRODUCED #50: 2MB 分节通过 ProfileFromMap 注册（无长度上限 → 财务 DoS）")
	}

	// 构造层：NewProfile 拒绝超长分节。
	if p := domain.NewProfile(domain.UserID("dos_attacker"),
		map[domain.SectionName]string{"skills": huge}, nil); p.ID != "" {
		t.Fatal("REPRODUCED #50: 2MB 分节通过 NewProfile 构造")
	}

	// pipeline 入口（直接构造的 Profile）：fail-loud 先于 LLM/embed 花费。
	cfg, err := config.Load("", nil)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	deps := pipeline.Deps{LLM: &signal.FakeLLM{}, Embedder: signal.FakeEmbedder{}}
	_, err = pipeline.RunFullMatch(pipeline.FullMatchInput{
		Profiles: []domain.Profile{
			domain.Profile{ID: "dos_attacker", Sections: map[domain.SectionName]string{"skills": huge}},
			domain.Profile{ID: "normal_user", Sections: map[domain.SectionName]string{"skills": "rust"}},
		},
	}, cfg, deps)
	if err == nil || !strings.Contains(err.Error(), "超长") {
		t.Fatalf("REPRODUCED #50: 超长分节未在 pipeline 入口 fail-loud（err=%v）", err)
	}

	// query 路径：超长 query 文本同样 fail-loud（广播到全部 section 名
	// 并全文进入 extract/embedding）。
	pool := &domain.EmbeddingsBundle{
		UserIDs:      []domain.UserID{"pool_1"},
		SectionNames: []domain.SectionName{"skills"},
		Embeddings:   domain.EmbeddingTensor{domain.UserEmbeddings{domain.SectionEmbeddings{domain.Vector{1}}}},
		Hyde:         map[domain.SectionName][][]domain.Vector{},
		Dim:          1, EmbeddingModel: "rt3",
	}
	_, err = pipeline.RunQueryMatch(pipeline.QueryMatchInput{
		QueryText:  huge,
		PoolBundle: pool,
		PoolSections: []domain.ExtractedSections{domain.ExtractedSections{
			ID: "pool_1", Sections: map[domain.SectionName]string{"skills": "rust"},
		}},
	}, cfg, deps)
	if err == nil || !strings.Contains(err.Error(), "query text") {
		t.Fatalf("REPRODUCED #50: 超长 query 未 fail-loud（err=%v）", err)
	}

	// 合法长度不受影响。
	if _, err := domain.ProfileFromMap(map[string]any{
		"id":       "normal_user",
		"sections": map[string]any{"skills": strings.Repeat("a", domain.MaxSectionLen)},
	}); err != nil {
		t.Fatalf("场景失效：恰好等于上限的合法分节被误拒（%v）", err)
	}
}
