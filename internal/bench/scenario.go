// 三场景 bench：数据加载（保序 JSON）+ 运行链路。
//
// 保序是正确性前提：噪声 RNG 流按 member × pool 的**文件序**消费，
// Go map 迭代无序，因此用 json.Decoder 逐 token 解析保持对象键序。
package bench

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/engine"
	"github.com/Cloudbird-Software/mutual/internal/signal"
)

// ScenarioData 是一个场景的完整数据（data/bench/{name}.json）。
type ScenarioData struct {
	Scenario      string
	Description   string
	EmbeddingOnly bool
	// Members / Pool 按文件序（噪声流依赖该顺序）。
	Members []signal.OrderedSections
	Pool    []signal.OrderedSections
	// GroundTruth: member_id → pool_id（黄金真值对）。
	GroundTruth map[string]string
}

// OrderedSectionsMap 解析 JSON object 并保持键的文件序。
func OrderedSectionsMap(raw json.RawMessage) ([]signal.OrderedSections, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("期望 JSON object")
	}
	var out []signal.OrderedSections
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := keyTok.(string)
		var sections map[string]string
		if err := dec.Decode(&sections); err != nil {
			return nil, fmt.Errorf("成员 %q: %w", key, err)
		}
		out = append(out, signal.OrderedSections{ID: key, Sections: sections})
	}
	// 消费收尾 '}'。
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}

// LoadScenario 加载场景数据（dataDir 为空时用 DefaultDataDir）。
func LoadScenario(name string, dataDir string) (*ScenarioData, error) {
	known := false
	for _, n := range ScenarioNames {
		if n == name {
			known = true
			break
		}
	}
	if !known {
		return nil, fmt.Errorf("未知场景 %q，可选: %v", name, ScenarioNames)
	}
	if dataDir == "" {
		dataDir = DefaultDataDir()
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, name+".json"))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Scenario      string            `json:"scenario"`
		Description   string            `json:"description"`
		EmbeddingOnly bool              `json:"embedding_only"`
		Members       json.RawMessage   `json:"members"`
		Pool          json.RawMessage   `json:"pool"`
		GroundTruth   map[string]string `json:"ground_truth"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("场景 %s: %w", name, err)
	}
	members, err := OrderedSectionsMap(doc.Members)
	if err != nil {
		return nil, fmt.Errorf("场景 %s members: %w", name, err)
	}
	pool, err := OrderedSectionsMap(doc.Pool)
	if err != nil {
		return nil, fmt.Errorf("场景 %s pool: %w", name, err)
	}
	return &ScenarioData{
		Scenario:      doc.Scenario,
		Description:   doc.Description,
		EmbeddingOnly: doc.EmbeddingOnly,
		Members:       members,
		Pool:          pool,
		GroundTruth:   doc.GroundTruth,
	}, nil
}

// ScenarioOptions 是 RunScenario 的可调参数（零值 = Python 默认）。
type ScenarioOptions struct {
	Seed        int
	NoiseScale  float64
	BMax        int
	PoolBMax    int
	NoPoolLimit bool
	DataDir     string
}

// RunScenario 跑单个场景：surrogate 打分 → pre_matrix → solve_match
// → 评测。推荐列表 = member 的匹配边按 final_weight 降序（求解器
// 实际输出），求解器退化直接压低 HR/NDCG。
func RunScenario(name string, opts ScenarioOptions) (domain.EvaluationReport, error) {
	if opts.NoiseScale == 0 {
		opts.NoiseScale = 0.24
	}
	if opts.BMax == 0 {
		opts.BMax = 3
	}
	poolBMax := 1
	if opts.NoPoolLimit {
		poolBMax = 0
	} else if opts.PoolBMax > 0 {
		poolBMax = opts.PoolBMax
	}

	data, err := LoadScenario(name, opts.DataDir)
	if err != nil {
		return domain.EvaluationReport{}, err
	}

	sseed := opts.Seed + scenarioSeedOffset[name]
	scores := signal.ScoreMatrix(data.Members, data.Pool, sseed, opts.NoiseScale, data.EmbeddingOnly)

	memberIDs := make([]domain.UserID, len(data.Members))
	for i, m := range data.Members {
		memberIDs[i] = domain.UserID(m.ID)
	}
	poolIDs := make([]domain.UserID, len(data.Pool))
	for j, p := range data.Pool {
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
		for pid, sc := range row {
			i, okI := mIdx[domain.UserID(mid)]
			j, okJ := pIdx[domain.UserID(pid)]
			if !okI || !okJ {
				continue
			}
			pm.PrefLeftToRight[i][j] = sc.AToB
			pm.PrefRightToLeft[j][i] = sc.BToA
		}
	}

	var poolPtr *int
	if poolBMax > 0 {
		poolPtr = &poolBMax
	}
	outcome := engine.SolveMatch(pm,
		engine.MatchingConfig{BMax: opts.BMax, PoolBMax: poolPtr},
		engine.BlendingConfig{EmbedWeight: 0.5, LLMWeight: 0.5},
	)

	// 推荐列表：member 的匹配边按 final_weight 降序（求解器输出）。
	truth := map[domain.UserID]domain.UserID{}
	for k, v := range data.GroundTruth {
		truth[domain.UserID(k)] = domain.UserID(v)
	}
	predictions, groundTruth := rankedByLeft(outcome.Edges, memberIDs, truth, maxInt(opts.BMax, 1))
	return engine.Evaluate(engine.EvaluateInput{
		Predictions: predictions,
		GroundTruth: groundTruth,
		PrefMatrix:  pm,
		MatchProb:   outcome.MatchProb,
	})
}

// RunScenarios 跑全部三场景。
//
// 默认 noiseScale=0.24：经数值标定，聚合 HR@3≈0.96、envy=1——门禁
// （0.6/0.4/2）有余量通过，且 classic 场景存在真实判别度。
func RunScenarios(seed int, noiseScale float64) (map[string]domain.EvaluationReport, error) {
	out := map[string]domain.EvaluationReport{}
	for _, name := range ScenarioNames {
		r, err := RunScenario(name, ScenarioOptions{Seed: seed, NoiseScale: noiseScale})
		if err != nil {
			return nil, err
		}
		out[name] = r
	}
	return out, nil
}

// AggregateReports 按场景数加权聚合（HR/NDCG 加权平均，envy 求和）。
func AggregateReports(reports []domain.EvaluationReport, names []string) domain.EvaluationReport {
	total := 0
	for _, r := range reports {
		total += r.TotalScenarios
	}
	if total == 0 {
		return domain.EvaluationReport{}
	}
	var hr1, hr3, hr5, ndcg float64
	for _, r := range reports {
		w := float64(r.TotalScenarios) / float64(total)
		hr1 += r.HRAt1 * w
		hr3 += r.HRAt3 * w
		hr5 += r.HRAt5 * w
		ndcg += r.NDCGAt5 * w
	}
	envyL, envyR := 0, 0
	for _, r := range reports {
		envyL += r.EnvyCountLeft
		envyR += r.EnvyCountRight
	}
	perScenario := map[string]any{}
	for i, r := range reports {
		key := fmt.Sprintf("%d", i)
		if i < len(names) {
			key = names[i]
		}
		perScenario[key] = r.ToMap()
	}
	return domain.EvaluationReport{
		HRAt1:          hr1,
		HRAt3:          hr3,
		HRAt5:          hr5,
		NDCGAt5:        ndcg,
		EnvyCountLeft:  envyL,
		EnvyCountRight: envyR,
		TotalScenarios: total,
		Metadata:       map[string]any{"per_scenario": perScenario},
	}
}

// RunSuite 完整评测套件：三场景 + 合成市场（market 只贡献 envy 门禁
// 信号）。CLI 以三场景聚合做 HR/NDCG 门禁、全部四项做 envy 门禁。
func RunSuite(seed int, noiseScale float64) (map[string]domain.EvaluationReport, error) {
	out := map[string]domain.EvaluationReport{}
	scenarios, err := RunScenarios(seed, noiseScale)
	if err != nil {
		return nil, err
	}
	for _, name := range ScenarioNames {
		r := scenarios[name]
		meta := map[string]any{}
		for k, v := range r.Metadata {
			meta[k] = v
		}
		meta["scenario"] = name
		r.Metadata = meta
		out[name] = r
	}
	market, err := RunBench(MarketOptions{Seed: seed})
	if err != nil {
		return nil, err
	}
	meta := map[string]any{}
	for k, v := range market.Metadata {
		meta[k] = v
	}
	meta["scenario"] = "market"
	market.Metadata = meta
	out["market"] = market
	return out, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
