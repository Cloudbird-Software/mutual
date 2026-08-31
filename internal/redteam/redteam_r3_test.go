// RT2/RT3 轮次 UserID 契约（issues #43/#51/#53/#55/#58）的回归复现。
//
// 约定同 redteam_test.go：每个测试对应一个 issue。漏洞为真 → 修复后
// 转绿常驻 CI；不成立 → 钉住"不可利用"的事实。
package redteam

import (
	"regexp"
	"strings"
	"testing"

	"github.com/Cloudbird-Software/mutual/config"
	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/engine"
	"github.com/Cloudbird-Software/mutual/internal/pipeline"
	"github.com/Cloudbird-Software/mutual/internal/signal"
	"github.com/Cloudbird-Software/mutual/internal/store"
)

// captureLLM 记录收到的各阶段 prompt（路由无关，响应固定）。
type captureLLM struct {
	extractPrompts []string
	scorePrompts   []string
}

func (c *captureLLM) CompleteExtract(prompt, model string) (string, error) {
	c.extractPrompts = append(c.extractPrompts, prompt)
	return `{"skills":"x","vision":"x","project":"x","needs":"x"}`, nil
}

func (c *captureLLM) CompleteScore(prompt, model string) (string, error) {
	c.scorePrompts = append(c.scorePrompts, prompt)
	return `[{"a_to_b":0.5,"b_to_a":0.5},{"a_to_b":0.5,"b_to_a":0.5}]`, nil
}

func (c *captureLLM) CompleteHyde(prompt, model string) (string, error) { return `["x"]`, nil }

func (c *captureLLM) CompleteIntroduce(prompt, model string) (string, error) {
	return `{"intro":"x","starter_topics":"x"}`, nil
}

// profileFromMap 是 ProfileFromMap 的薄封装（表驱动用）。
func profileFromMap(t *testing.T, id string, sections map[string]any) (domain.Profile, error) {
	t.Helper()
	return domain.ProfileFromMap(map[string]any{"id": id, "sections": sections})
}

// ---------------------------------------------------------------------------
// #43 / #51 / #53 / #58：UserID 字符集统一白名单（注册咽喉）
// ---------------------------------------------------------------------------

// TestRT2_UserIDCharsetInjectionSurface #43：冒号/井号/方括号/反引号/
// 管道符/尖括号/RTL/零宽等夹缝字符不得通过注册校验。
func TestRT2_UserIDCharsetInjectionSurface(t *testing.T) {
	rejected := []string{
		"user:admin",   // 冒号（#43/#53/#58）
		"evil mirror",  // 空格（#51）
		"mallory:corp", // 夹缝 ID（#58）
		"user###2",     // 井号（### Pair 块头伪造面）
		"user[0]",      // 方括号（JSON 结构面）
		"user`code`",   // 反引号（markdown 围栏面）
		"user|pipe",    // 管道符（|text_block 渲染面）
		"<script>",     // 尖括号（HTML/prompt injection 面）
		"user​zwsp",    // 零宽空格（视觉隐藏）
		"user‮rtl",     // RTL 覆盖（视觉伪造）
		"中文名",          // 非 ASCII（Unicode 内容面）
		"a__b",         // "__"（PairID 分隔符碰撞，#55）
		"a..b",         // ".."（路径穿越）
		".hidden",      // 点开头（隐藏文件）
		strings.Repeat("a", domain.MaxUserIDLen+1), // 超长（#53 投递面）
	}
	for _, id := range rejected {
		if domain.ValidUserID(domain.UserID(id)) {
			t.Fatalf("REPRODUCED #43: 夹缝 ID %q 通过 ValidUserID 白名单", id)
		}
		if _, err := profileFromMap(t, id, map[string]any{"skills": "s"}); err == nil {
			t.Fatalf("REPRODUCED #43: 夹缝 ID %q 通过注册校验（ProfileFromMap）", id)
		}
		if p := domain.NewProfile(domain.UserID(id), nil, nil); p.ID != "" {
			t.Fatalf("REPRODUCED #43: 夹缝 ID %q 通过 NewProfile 构造", id)
		}
	}
	accepted := []string{"alice", "user-1", "a.b_c", "000_attack", "mallory_corp"}
	for _, id := range accepted {
		if !domain.ValidUserID(domain.UserID(id)) {
			t.Fatalf("场景失效：合法 ID %q 被误拒", id)
		}
	}
}

// TestRT3_SpaceUserIDScoringDenialBypassesGate #51：空格 ID 在注册层
// 拒绝；同时钉住"白名单 ID 渲染的打分块头恒可被 pairHeaderRE
// （[^,\s]+）反向解析"——渲染与解析契约不再脱节。
func TestRT3_SpaceUserIDScoringDenialBypassesGate(t *testing.T) {
	if _, err := profileFromMap(t, "evil mirror", map[string]any{"skills": "x"}); err == nil {
		t.Fatalf("REPRODUCED #51: 空格 ID 通过注册校验，可触发整批 unscored 绕过 verifiability gate")
	}

	// 渲染侧：合法 ID 的块头必须匹配 bamlllm.pairHeaderRE 的形状
	// （用户段无空白/逗号/括号 → 恒可解析，解析失败型 DoS 不可构造）。
	headerRE := regexp.MustCompile(`(?m)^### Pair \d+: \([^,\s]+, [^)\s]+\)$`)
	llm := &captureLLM{}
	sections := map[domain.UserID]map[string]string{}
	for _, id := range []domain.UserID{"evil_mirror", "honest_member", "target"} {
		sections[id] = map[string]string{"skills": "s", "needs": "n", "project": "p", "vision": "v"}
	}
	batch := []domain.CandidatePair{
		domain.NewCandidatePair("evil_mirror", "target", 0.9),
		domain.NewCandidatePair("honest_member", "target", 0.9),
	}
	engine.ScorePairs(batch, sections, "score.", scoringTemplate(t), llm,
		engine.ScoreBudgets{BatchSize: 2})
	if len(llm.scorePrompts) != 1 {
		t.Fatalf("场景失效：期望 1 次批量打分调用，got %d", len(llm.scorePrompts))
	}
	headers := headerRE.FindAllString(llm.scorePrompts[0], -1)
	if len(headers) != 2 {
		t.Fatalf("REPRODUCED #51: 批量块头不可反向解析（匹配 %d/2）——渲染/解析契约脱节: %q",
			len(headers), llm.scorePrompts[0])
	}
}

// TestRT3_UserIDContentDelivery #53：内容型 ID（钓鱼话术载体）在注册层
// 拒绝，平台代发话术/报告的 partner 槽位不再可能内嵌攻击者内容。
func TestRT3_UserIDContentDelivery(t *testing.T) {
	phishID := "urgent: verify account at http://evil.example now"
	if _, err := profileFromMap(t, phishID, map[string]any{"skills": "s"}); err == nil {
		t.Fatalf("REPRODUCED #53: 钓鱼内容 ID %q 通过注册校验，可借平台话术投递", phishID)
	}
	// 直接构造（绕过注册）也被 pipeline 入口 fail-loud 拒绝（见 #58 测试）。
	if domain.ValidUserID(domain.UserID(phishID)) {
		t.Fatalf("REPRODUCED #53: 内容型 ID 满足 ValidUserID")
	}
}

// ---------------------------------------------------------------------------
// #55：StablePairID "__" 分隔符歧义 → 跨用户 PairID 碰撞
// ---------------------------------------------------------------------------

// TestRT3_PairIDSeparatorCollision #55：StablePairID 的 "__" 连接在 ID
// 含 "__" 时非单射（函数层事实）；修复 = ID 契约禁止 "__"，注册层
// 消灭碰撞构造材料，并钉住"无 '__' ID 的连接恒单射"性质。
func TestRT3_PairIDSeparatorCollision(t *testing.T) {
	// 函数层歧义仍然存在（PairID 磁盘格式是既有契约，不改动）：
	// 碰撞只能由含 "__" 的 ID 构造——而这类 ID 现已被注册契约拒绝。
	p1 := domain.StablePairID("a", "b__c")
	p2 := domain.StablePairID("a__b", "c")
	if p1 != p2 {
		t.Fatalf("场景失效：未发生碰撞（%q != %q）", p1, p2)
	}
	for _, id := range []string{"a__b", "b__c", "x__y"} {
		if _, err := profileFromMap(t, id, map[string]any{"skills": "s"}); err == nil {
			t.Fatalf("REPRODUCED #55: 含 \"__\" 的 ID %q 通过注册校验，可构造 PairID 碰撞", id)
		}
	}

	// 单射性质：合法 ID（不含 "__"）集合上，PairID → 无序对 是单射。
	ids := []string{"a", "a_b", "a.b", "b", "b_c", "c", "c.d-e", "0", "zzz"}
	norm := func(x, y string) [2]string {
		if x > y {
			x, y = y, x
		}
		return [2]string{x, y}
	}
	seen := map[domain.PairID][2]string{}
	for i := range ids {
		for j := range ids {
			if i == j {
				continue
			}
			pid := domain.StablePairID(domain.UserID(ids[i]), domain.UserID(ids[j]))
			pair := norm(ids[i], ids[j])
			if prev, dup := seen[pid]; dup && prev != pair {
				t.Fatalf("REPRODUCED #55: 合法 ID 集合内 PairID 碰撞: %q = %v / %v",
					pid, prev, pair)
			}
			seen[pid] = pair
		}
	}
}

// ---------------------------------------------------------------------------
// #58：注册层/存储层校验分裂 → 全员批次 DoS
// ---------------------------------------------------------------------------

// TestRT3_StoreModeRegistrationDoS #58：夹缝 ID（如 "mallory:corp"）在
// 注册层拒绝；直接构造 Profile 的调用方在 RunFullMatch 入口 fail-loud
// 拒绝（先于任何 LLM/embed 花费），不再演化为 PutSections 处的全员失败。
func TestRT3_StoreModeRegistrationDoS(t *testing.T) {
	cfg, err := config.Load("", nil)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	mk := func(t *testing.T, id string) domain.Profile {
		t.Helper()
		p, err := profileFromMap(t, id, map[string]any{
			"skills": "rust kubernetes cloud", "vision": "efficient cloud infra",
			"project": "cloud cost platform", "needs": "kubernetes experts",
		})
		if err != nil {
			t.Fatalf("场景失效：合法 ID %q 被误拒: %v", id, err)
		}
		return p
	}

	// 夹缝 ID 在注册层即被拒绝（攻击材料不可获得）。
	if _, err := profileFromMap(t, "mallory:corp", map[string]any{"skills": "s"}); err == nil {
		t.Fatalf("REPRODUCED #58: 夹缝 ID 通过注册校验")
	}

	deps := func(root string) pipeline.Deps {
		fs, err := store.NewFileStore(root, 6)
		if err != nil {
			t.Fatalf("filestore: %v", err)
		}
		return pipeline.Deps{LLM: &signal.FakeLLM{}, Embedder: signal.FakeEmbedder{}, Store: fs}
	}

	// 基线：全员合法 ID 时 Store 模式运行成功。
	if _, err := pipeline.RunFullMatch(pipeline.FullMatchInput{
		Profiles: []domain.Profile{mk(t, "alice"), mk(t, "bob")},
	}, cfg, deps(t.TempDir())); err != nil {
		t.Fatalf("场景失效：基线运行失败: %v", err)
	}

	// 攻击（直接构造、绕过注册的夹缝 ID）：入口即拒，且先于 LLM 调用。
	capture := &captureLLM{}
	_, err = pipeline.RunFullMatch(pipeline.FullMatchInput{
		Profiles: []domain.Profile{
			mk(t, "alice"), mk(t, "bob"),
			{ID: "mallory:corp", Sections: map[domain.SectionName]string{"skills": "x"}},
		},
	}, cfg, pipeline.Deps{LLM: capture, Embedder: signal.FakeEmbedder{},
		Store: func() *store.FileStore {
			fs, e := store.NewFileStore(t.TempDir(), 6)
			if e != nil {
				t.Fatalf("filestore: %v", e)
			}
			return fs
		}()})
	if err == nil {
		t.Fatalf("REPRODUCED #58: 夹缝 ID 直接构造进入 RunFullMatch 未被拒绝（将炸掉全员批次）")
	}
	if !strings.Contains(err.Error(), "mallory:corp") {
		t.Fatalf("拒绝错误未指明违规 ID: %v", err)
	}
	if len(capture.extractPrompts) != 0 {
		t.Fatalf("拒绝发生在 LLM 花费之后（extract 调用 %d 次）", len(capture.extractPrompts))
	}
}
