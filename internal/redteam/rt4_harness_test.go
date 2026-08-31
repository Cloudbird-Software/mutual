// RT4（Round 4）红队轮次共享 Harness：基线档案 + Mock API 封装。
//
// 背景：互惠推荐引擎已历 RT-2026-08（#27-#38，已合入）、RT2（#40-#48）、
// RT3（#49-#58，修复 PR #59-#62 未合入 main）。本轮（RT4）以"深度敌意
// 视角"探查提示词注入与信息篡改边界，寻找**不在 #40-#58 清单内**的新向量。
//
// Mock API 语义（spec/04-fixtures.md §7）：FakeLLM 按 "### Pair N: (u1,u2)"
// 块头查表打分、非打分类路径返回固定 intro JSON；FakeEmbedder 按文本
// content-addressed 确定性 128 维向量。recordingLLM 在 FakeLLM 之上记录
// 全部 prompt，用于注入/解析类验证。
package redteam

import (
	"strings"
	"testing"

	"github.com/Cloudbird-Software/mutual/config"
	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/engine"
	"github.com/Cloudbird-Software/mutual/internal/signal"
)

// rt4Templates 解析四类内置 prompt 模板。
func rt4Templates(t *testing.T) map[string]string {
	t.Helper()
	cfg, err := config.Load("", nil)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	tpl, err := cfg.ResolvePromptTemplates(nil)
	if err != nil {
		t.Fatalf("templates: %v", err)
	}
	return tpl
}

// recordingLLM 记录所有 prompt，同时像 FakeLLM 一样按块头路由打分。
type recordingLLM struct {
	fake    *signal.FakeLLM
	prompts []string
}

func newRecordingLLM() *recordingLLM { return &recordingLLM{fake: &signal.FakeLLM{}} }

func (r *recordingLLM) CompleteScore(prompt, model string) (string, error) {
	r.prompts = append(r.prompts, "[scoring]\n"+prompt)
	return r.fake.CompleteScore(prompt, model)
}
func (r *recordingLLM) CompleteExtract(prompt, model string) (string, error) {
	r.prompts = append(r.prompts, "[extract]\n"+prompt)
	return r.fake.CompleteExtract(prompt, model)
}
func (r *recordingLLM) CompleteHyde(prompt, model string) (string, error) {
	r.prompts = append(r.prompts, "[hyde]\n"+prompt)
	return r.fake.CompleteHyde(prompt, model)
}
func (r *recordingLLM) CompleteIntroduce(prompt, model string) (string, error) {
	r.prompts = append(r.prompts, "[introduce]\n"+prompt)
	return r.fake.CompleteIntroduce(prompt, model)
}

func (r *recordingLLM) lastHydePrompt() string {
	for i := len(r.prompts) - 1; i >= 0; i-- {
		if strings.HasPrefix(r.prompts[i], "[hyde]") {
			return r.prompts[i]
		}
	}
	return ""
}

// rt4BaselineSections 是模拟恶意参与者的"真实、合规"基线档案（原始锚点）。
// 参与者：海南自贸港跨境贸易合规顾问。资质、经历、需求均真实，无欺诈成分；
// 后续"欺诈放大"均以此为锚点篡改。
func rt4BaselineSections() map[domain.SectionName]string {
	return map[domain.SectionName]string{
		"skills":  "海南自贸港跨境贸易政策咨询；进出口合规审查；报关报检流程；RCEP 原产地规则运用",
		"vision":  "帮助内地中小企业借助海南自贸港与 RCEP 红利，合规出海东盟市场",
		"project": "正在为海南某外贸企业搭建跨境电商合规与退税体系咨询方案",
		"needs":   "寻找长三角跨境电商卖家和海外仓运营方，合作落地对东盟的合规出口方案",
	}
}

// rt4HonestSections 是诚实目标用户（对照组，真实合规画像）。
func rt4HonestSections() map[domain.SectionName]string {
	return map[domain.SectionName]string{
		"skills":  "跨境电商平台运营；海外仓管理；欧美与东南亚物流履约；供应链金融",
		"vision":  "把中国供应链能力通过合规渠道卖向全球，尤其是东南亚新兴市场",
		"project": "在宁波运营三个海外仓，正在拓展东南亚仓储网络与本地化服务",
		"needs":   "寻找熟悉东南亚合规报关的咨询伙伴，一起把货卖进东盟",
	}
}

// rt4Recipe 复刻 default.yaml 的相似度配方。
func rt4Recipe() engine.RecipeConfig {
	return engine.RecipeConfig{
		SectionWeights: map[string]float64{
			"skills": -0.10, "vision": 0.35, "project": 0.25, "needs": -0.10,
		},
		CrossSectionWeights: []engine.WeightEntry{
			{Key: "needs_skills", Value: 0.80},
		},
	}
}

// rt4EmbedAndSimilarity 跑 hyde → embed → similarity 最小链路，返回结果。
// useHyde=false 时跳过 hyde（对照组：无描述符碰撞的真实内容相似度）。
func rt4EmbedAndSimilarity(t *testing.T, sections map[domain.UserID]map[domain.SectionName]string, useHyde bool) (*domain.SimilarityResult, *recordingLLM, error) {
	t.Helper()
	var esList []domain.ExtractedSections
	for id, secs := range sections {
		esList = append(esList, domain.NewExtractedSections(id, secs, ""))
	}
	tpl := rt4Templates(t)
	rec := newRecordingLLM()
	hyde := map[domain.UserID]domain.HydeDescriptors{}
	if useHyde {
		hyde = engine.GenerateHyde(esList, 1, tpl[config.TemplateHyde], "test-model", rec)
	}
	bundle, err := engine.EmbedSections(esList, hyde, "test-model", nil, signal.FakeEmbedder{})
	if err != nil {
		return nil, nil, err
	}
	return engine.ComputeSimilarity(bundle, nil, rt4Recipe()), rec, nil
}

// rt4MatIdx 返回 sections map 中 id 对应的行列序号（键序：插入序，确定）。
func rt4MatIdx(sections map[domain.UserID]map[domain.SectionName]string, id domain.UserID) int {
	i := 0
	for k := range sections {
		if k == id {
			return i
		}
		i++
	}
	return -1
}
