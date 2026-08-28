// Package advprobe 是临时安全审查探针（红队实验，审查结束后删除）。
//
// 实验 1（确定性）：用 signal.Surrogate（token 重叠式 LLM 替身）+
// engine.SolveMatch，测量恶意画像在合成市场（classic bench）中
// 骗取的不对等匹配收益与对诚实用户的挤出效应。
package advprobe

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Cloudbird-Software/mutual/internal/bench"
	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/engine"
	"github.com/Cloudbird-Software/mutual/internal/signal"
)

// honestZoe 是攻击者的基本事实（ground truth）。
var honestZoe = map[string]string{
	"skills":  "basic react html css junior frontend forms",
	"vision":  "grow into fullstack engineer",
	"project": "company internal forms tool react html css simple form pages",
	"needs":   "need senior mentor to learn backend and databases",
}

// variants 是攻击载荷（红队子代理设计）。
var variants = []struct {
	ID       string
	Label    string
	Sections map[string]string
}{
	{"V0", "honest-baseline", honestZoe},
	{"V1", "exaggeration", map[string]string{
		"skills":  "senior fullstack engineer react typescript html css node postgres graphql rust blockchain services pytorch nlp finetuning evaluation swift visionos spatial ui distributed systems performance system design mentoring",
		"vision":  "ship end to end products across web infrastructure practical ai and immersive learning, grow into principal engineer",
		"project": "leading company wide internal forms and workflow platform react typescript frontend rust microservices backend, 40k users, rebuilding settlement workflow engine for audit hardening, adding pytorch document routing with labeled dataset and evaluation criteria, prototyping visionos spatial training client",
		"needs":   "need rust formal verification expert to audit settlement workflow engine, pytorch specialist for document routing evaluation, visionos developer for spatial training client, database architecture guidance from senior engineers",
	}},
	{"V2", "keyword-stuffing", map[string]string{
		"skills":  "react html css forms typescript javascript rust blockchain consensus performance formal verification pytorch nlp transformers gpu finetuning evaluation kubernetes terraform aws finops automation cost fpga verilog dsp radar signal processing timing grant writing nih clinical trials irb compliance swift visionos arkit realitykit spatial ui supply chain forecasting python optimization simulation accessibility wcag design systems distributed systems cuda tensorrt inference serving",
		"vision":  "grow from frontend into fullstack engineer",
		"project": "company internal forms tool react html css simple form pages",
		"needs":   "need senior mentor to learn backend and databases",
	}},
	{"V3", "market-mirror", map[string]string{
		"skills":  "react html css typescript node rust blockchain consensus performance formal verification pytorch nlp transformers gpu finetuning evaluation kubernetes terraform aws finops automation cost fpga verilog dsp radar signal processing timing grant writing nih clinical trials irb compliance swift visionos arkit realitykit spatial ui supply chain forecasting python optimization simulation accessibility wcag design systems distributed systems data pipelines mentoring",
		"vision":  "decentralized resilient infrastructure accessible tools for everyone practical ai for regulated industries efficient cloud engineering safer autonomous sensing evidence based healthcare at scale immersive learning environments resilient data driven supply chains growing into fullstack engineer",
		"project": "company internal forms tool react html css simple form pages",
		"needs":   "need rust blockchain consensus audit formal verification react accessibility wcag design systems pytorch nlp finetuning evaluation kubernetes terraform aws finops fpga verilog dsp radar signal processing nih grant writing irb clinical trials swift visionos arkit realitykit spatial ui supply chain forecasting python optimization gpu inference optimization senior fullstack mentor backend databases",
	}},
	// V4/V5：自适应定向攻击——攻击者可见市场画像（pool 侧公开是常态），
	// 用最小 token 集精准模仿目标。V4 单目标 p2，V5 三目标 p0/p2/p6。
	{"V4", "targeted-single-p2", map[string]string{
		"skills":  "domain datasets clear evaluation criteria nlp finetuning benchmarks pytorch transformers",
		"vision":  "practical nlp for regulated industries",
		"project": "pytorch nlp transformers gpu finetuning evaluation",
		"needs":   "pytorch nlp transformers gpu finetuning evaluation",
	}},
	{"V5", "targeted-triple", map[string]string{
		"skills":  "hard distributed systems problems domain datasets clear evaluation criteria 3d content learning scenarios",
		"vision":  "practical nlp for regulated industries",
		"project": "rust blockchain consensus pytorch nlp transformers swift visionos arkit",
		"needs":   "rust blockchain consensus performance formal verification pytorch nlp transformers gpu finetuning evaluation swift visionos arkit realitykit spatial ui",
	}},
}

// runMarket 跑一次 market：surrogate 打分 → pre_matrix → SolveMatch。
func runMarket(t *testing.T, sc *bench.ScenarioData, att map[string]string) map[string]any {
	t.Helper()
	members := append([]signal.OrderedSections{}, sc.Members...)
	if att != nil {
		members = append(members, signal.OrderedSections{ID: "zoe", Sections: att})
	}
	scores := signal.ScoreMatrix(members, sc.Pool, 42, 0.0, false)

	memberIDs := make([]domain.UserID, len(members))
	for i, m := range members {
		memberIDs[i] = domain.UserID(m.ID)
	}
	poolIDs := make([]domain.UserID, len(sc.Pool))
	for j, p := range sc.Pool {
		poolIDs[j] = domain.UserID(p.ID)
	}
	mIdx := map[domain.UserID]int{}
	for i, id := range memberIDs {
		mIdx[id] = i
	}
	pIdx := map[domain.UserID]int{}
	for j, id := range poolIDs {
		pIdx[id] = j
	}

	pm := domain.NewPrefMatrix(memberIDs, poolIDs)
	for mid, row := range scores {
		for pid, s := range row {
			i, okI := mIdx[domain.UserID(mid)]
			j, okJ := pIdx[domain.UserID(pid)]
			if !okI || !okJ {
				continue
			}
			pm.PrefLeftToRight[i][j] = s.AToB
			pm.PrefRightToLeft[j][i] = s.BToA
		}
	}

	poolBMax := 1
	outcome := engine.SolveMatch(pm,
		engine.MatchingConfig{BMax: 3, PoolBMax: &poolBMax},
		engine.BlendingConfig{EmbedWeight: 0.5, LLMWeight: 0.5},
	)

	// 黄金对保持情况（对市场的破坏度）。
	edgeSet := map[string]bool{}
	for _, e := range outcome.Edges {
		edgeSet[string(e.User1)+"|"+string(e.User2)] = true
	}
	goldenKept := 0
	var goldenLost []string
	for k, v := range sc.GroundTruth {
		if edgeSet[k+"|"+v] {
			goldenKept++
		} else {
			goldenLost = append(goldenLost, k+"→"+v)
		}
	}

	result := map[string]any{
		"total_edges":   len(outcome.Edges),
		"golden_kept":   goldenKept,
		"golden_lost":   goldenLost,
		"envy":          outcome.EnvyReport["total_envy"],
		"zoe_edges":     []any{},
		"pool_partners": map[string]string{},
	}

	// pool 侧的归属（谁抢到了谁）。
	poolOwner := map[string]string{}
	for _, e := range outcome.Edges {
		poolOwner[string(e.User2)] = string(e.User1)
	}
	result["pool_partners"] = poolOwner

	if att != nil {
		zoeEdges := []any{}
		poolSections := map[string]map[string]string{}
		for _, p := range sc.Pool {
			poolSections[p.ID] = p.Sections
		}
		for _, e := range outcome.Edges {
			if e.User1 != "zoe" {
				continue
			}
			pid := string(e.User2)
			perceivedAToB := scores["zoe"][pid].AToB // 系统认为 zoe 得到的价值
			perceivedBToA := scores["zoe"][pid].BToA // 系统认为 partner 得到的价值
			// 真实价值：以诚实画像计算。
			trueZoeGets := signal.DirectionalScore(honestZoe, poolSections[pid])
			truePGets := signal.DirectionalScore(poolSections[pid], honestZoe)
			zoeEdges = append(zoeEdges, map[string]any{
				"partner":           pid,
				"nsw":               e.FinalWeight,
				"perceived_a_to_b":  round3(perceivedAToB),
				"perceived_b_to_a":  round3(perceivedBToA),
				"true_zoe_gets":     round3(trueZoeGets),
				"true_partner_gets": round3(truePGets),
				"deception_gap":     round3(perceivedBToA - truePGets),
			})
		}
		result["zoe_edges"] = zoeEdges

		// 诊断：zoe 对全部 pool 的候选 nsw + 黄金对的 nsw（竞争态势）。
		zoeAll := []any{}
		for _, p := range sc.Pool {
			a := scores["zoe"][p.ID].AToB
			b := scores["zoe"][p.ID].BToA
			zoeAll = append(zoeAll, map[string]any{
				"partner": p.ID, "a_to_b": round3(a), "b_to_a": round3(b), "nsw": round3(sqrt(a * b)),
			})
		}
		result["zoe_all_pairs"] = zoeAll
		goldenNsw := []any{}
		for k, v := range sc.GroundTruth {
			a := scores[k][v].AToB
			b := scores[k][v].BToA
			goldenNsw = append(goldenNsw, map[string]any{
				"pair": k + "-" + v, "a_to_b": round3(a), "b_to_a": round3(b), "nsw": round3(sqrt(a * b)),
			})
		}
		result["golden_pair_nsw"] = goldenNsw
	}
	return result
}

func sqrt(f float64) float64 {
	if f <= 0 {
		return 0
	}
	x := f
	for i := 0; i < 40; i++ {
		x = (x + f/x) / 2
	}
	return x
}

func round3(f float64) float64 {
	return float64(int(f*1000+0.5)) / 1000
}

// TestAdvProbeSurrogate 实验 1：surrogate 信号源下的攻击收益。
func TestAdvProbeSurrogate(t *testing.T) {
	sc, err := bench.LoadScenario("classic", "")
	if err != nil {
		t.Fatalf("加载场景: %v", err)
	}

	out := map[string]any{}
	out["baseline_no_attacker"] = runMarket(t, sc, nil)
	for _, v := range variants {
		out[v.ID+"_"+v.Label] = runMarket(t, sc, v.Sections)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatalf("序列化: %v", err)
	}
	fmt.Printf("=== ADVPROBE SURROGATE RESULTS ===\n%s\n", string(b))
}
