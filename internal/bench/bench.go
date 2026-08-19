// Package bench 是互惠评测的离线 oracle 数据源（spec/03-oracles.md §3）。
//
// 两层数据源：
//
//  1. 合成市场（GenerateMarket / RunBench）：黄金对构造性 oracle，
//     守护 envy 门禁与评测链路自洽（HR 构造性 = 1.0）；
//  2. 三场景 bench（RunScenario / RunScenarios，data/bench/*.json）：
//     强模型标注的真实语义画像 + 黄金真值对——classic（经典互惠）/
//     drift（兴趣演化）/ cold（冷启动，仅 embedding 信号）。
//
// 三场景的推荐列表来自**求解器输出**（匹配边按权重排序），因此求解器
// 或打分链路的退化会直接传导到 HR/NDCG。信号源用 internal/signal
// （确定性 LLM/embedder 替身，带固定 seed 噪声模拟判断不完美），
// CI 无需真实凭据即可复现。
//
// 与 Python 基线（src/mutual/bench.py）逐位对齐：RNG 流的消费顺序
// （黄金对扰动 → 左侧噪声矩阵 → 右侧噪声矩阵；member × pool × 双向）
// 与 tuple 逆序排序语义（(-weight, pid) 反向 = weight 降序、pid 降序）
// 是两处最容易走样的边界，golden 差分测试（bench_golden_test.go）逐位
// 验证本包输出与 Python 基线的 evaluation_report.json 一致。
package bench

import (
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/Cloudbird-Software/mutual/internal/domain"
	"github.com/Cloudbird-Software/mutual/internal/engine"
	"github.com/Cloudbird-Software/mutual/internal/rng"
)

// ScenarioNames 是三场景 bench 的固定顺序。
var ScenarioNames = []string{"classic", "drift", "cold"}

// scenarioSeedOffset 是场景固定 seed 偏移（同一 seed 下各场景噪声
// 独立且跨版本稳定）。
var scenarioSeedOffset = map[string]int{"classic": 0, "drift": 101, "cold": 202}

// DefaultDataDir 定位三场景数据目录（data/bench）：从工作目录向上
// 逐级查找。CLI 在仓库根运行时命中 `data/bench`；包内测试从
// internal/bench 运行时上溯两级命中仓库根。
func DefaultDataDir() string {
	dir, err := os.Getwd()
	if err != nil {
		return filepath.Join("data", "bench")
	}
	for {
		cand := filepath.Join(dir, "data", "bench")
		if st, err := os.Stat(cand); err == nil && st.IsDir() {
			return cand
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Join("data", "bench")
		}
		dir = parent
	}
}

// GenerateMarket 生成确定性双边互惠偏好市场（构造性 oracle）。
//
// 构造规则（使存在清晰互惠最优解）：
//   - 黄金对 left i ↔ right i（i < min(M, N)）：双向偏好
//     0.9 + 0.09·((i+seed) % 5)，方向各带独立轻微扰动（A→B ≠ B→A）；
//   - 非黄金对：偏好 0.2 + 0.05·rand（低噪声），位于黄金偏好之下；
//   - 多余数量（M ≠ N 时）只贡献低偏好噪声候选，无黄金真值。
//
// RNG 流（与 Python 逐位一致）：先 n_gold 次 rand()（黄金对扰动），
// 再 M×N rand()（左→右噪声，row-major），再 N×M rand()（右→左噪声）。
func GenerateMarket(numLeft, numRight, seed int) *domain.PrefMatrix {
	rs := rng.New(uint32(uint32(seed)))
	idsLeft := make([]domain.UserID, numLeft)
	for i := range idsLeft {
		idsLeft[i] = domain.UserID(fmt.Sprintf("L%02d", i))
	}
	idsRight := make([]domain.UserID, numRight)
	for j := range idsRight {
		idsRight[j] = domain.UserID(fmt.Sprintf("R%02d", j))
	}

	prefLR := domain.NewMatrixZeros(numLeft, numRight)
	prefRL := domain.NewMatrixZeros(numRight, numLeft)
	for i := 0; i < numLeft; i++ {
		for j := 0; j < numRight; j++ {
			prefLR[i][j] = 0.2
		}
	}
	for j := 0; j < numRight; j++ {
		for i := 0; i < numLeft; i++ {
			prefRL[j][i] = 0.2
		}
	}

	nGold := min(numLeft, numRight)
	for i := 0; i < nGold; i++ {
		gold := 0.9 + 0.09*float64(mod5(i+seed))
		lr := gold
		rl := gold + 0.02*rs.Float64()
		prefLR[i][i] = lr
		prefRL[i][i] = rl
	}

	noiseLR := rs.Rand2(numLeft, numRight)
	noiseRL := rs.Rand2(numRight, numLeft)
	for i := 0; i < numLeft; i++ {
		for j := 0; j < numRight; j++ {
			prefLR[i][j] = clamp01(prefLR[i][j] + 0.05*noiseLR[i][j])
		}
	}
	for j := 0; j < numRight; j++ {
		for i := 0; i < numLeft; i++ {
			prefRL[j][i] = clamp01(prefRL[j][i] + 0.05*noiseRL[j][i])
		}
	}

	pm := domain.NewPrefMatrix(idsLeft, idsRight)
	pm.PrefLeftToRight = prefLR
	pm.PrefRightToLeft = prefRL
	return pm
}

func mod5(n int) int {
	m := n % 5
	if m < 0 {
		m += 5
	}
	return m
}

func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}

// GoldenTruth 返回黄金真值标记：left i ↔ right i（i < min(M, N)）。
func GoldenTruth(market *domain.PrefMatrix) map[domain.UserID]domain.UserID {
	n := min(len(market.LeftIDs), len(market.RightIDs))
	out := make(map[domain.UserID]domain.UserID, n)
	for i := 0; i < n; i++ {
		out[market.LeftIDs[i]] = market.RightIDs[i]
	}
	return out
}

// MarketOptions 是 RunBench 的可调参数（零值 = Python 默认）。
type MarketOptions struct {
	NumLeft     int
	NumRight    int
	Seed        int
	BMax        int
	PoolBMax    int // 传入 NoPoolLimit 表示不限
	NoPoolLimit bool
}

// RunBench 跑一轮合成市场评测：生成 → 求解 → 推荐列表（求解器输出）
// → evaluate。
//
// 构造性校验：HR@3 ≥ 0.99，否则返回错误（合成数据或匹配器回归，
// CI 门禁据此阻断）。
func RunBench(opts MarketOptions) (domain.EvaluationReport, error) {
	if opts.NumLeft == 0 {
		opts.NumLeft = 30
	}
	if opts.NumRight == 0 {
		opts.NumRight = 20
	}
	if opts.BMax == 0 {
		opts.BMax = 1
	}
	poolBMax := 1
	if opts.NoPoolLimit {
		poolBMax = 0 // MatchingConfig.PoolBMax = nil
	} else if opts.PoolBMax > 0 {
		poolBMax = opts.PoolBMax
	}

	market := GenerateMarket(opts.NumLeft, opts.NumRight, opts.Seed)
	truth := GoldenTruth(market)

	var poolPtr *int
	if poolBMax > 0 {
		poolPtr = &poolBMax
	}
	outcome := engine.SolveMatch(market,
		engine.MatchingConfig{BMax: opts.BMax, PoolBMax: poolPtr},
		engine.BlendingConfig{EmbedWeight: 0.5, LLMWeight: 0.5},
	)

	predictions, groundTruth := rankedByLeft(outcome.Edges, market.LeftIDs, truth, 5)
	report, err := engine.Evaluate(engine.EvaluateInput{
		Predictions: predictions,
		GroundTruth: groundTruth,
		PrefMatrix:  market,
		MatchProb:   outcome.MatchProb,
	})
	if err != nil {
		return report, err
	}
	if report.HRAt3 < 0.99 {
		return report, fmt.Errorf("bench 构筑性失败: HR@3=%.3f", report.HRAt3)
	}
	return report, nil
}

// rankedByLeft 从求解器输出构造每个左节点的推荐列表。
//
// 排序语义复刻 Python sorted(by_left[lid], reverse=True)：tuple
// (final_weight, pid) 逆序 = weight 降序、pid 降序（平局时字典序
// 大者在前）。topK 截断。
func rankedByLeft(edges []domain.Edge, leftIDs []domain.UserID, truth map[domain.UserID]domain.UserID, topK int) ([][]string, []string) {
	byLeft := map[domain.UserID][]domain.Edge{}
	for _, uid := range leftIDs {
		byLeft[uid] = nil
	}
	for _, e := range edges {
		if _, ok := byLeft[e.User1]; ok {
			byLeft[e.User1] = append(byLeft[e.User1], e)
		} else if _, ok := byLeft[e.User2]; ok {
			byLeft[e.User2] = append(byLeft[e.User2], e)
		}
	}

	var predictions [][]string
	var groundTruth []string
	for _, lid := range leftIDs {
		want, ok := truth[lid]
		if !ok {
			continue // 无真值：不算作评测场景
		}
		entries := append([]domain.Edge(nil), byLeft[lid]...)
		sortEdgesReverse(entries)
		ranked := make([]string, 0, len(entries))
		for _, e := range entries {
			ranked = append(ranked, partnerOf(e, lid))
		}
		if len(ranked) > topK {
			ranked = ranked[:topK]
		}
		predictions = append(predictions, ranked)
		groundTruth = append(groundTruth, string(want))
	}
	return predictions, groundTruth
}

// sortEdgesReverse 按 (final_weight desc, partner_id desc) 排序
// （Python tuple 逆序语义）。
func sortEdgesReverse(entries []domain.Edge) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && edgeGreater(entries[j], entries[j-1]); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

func edgeGreater(a, b domain.Edge) bool {
	if a.FinalWeight != b.FinalWeight {
		return a.FinalWeight > b.FinalWeight
	}
	return a.PairID > b.PairID
}

func partnerOf(e domain.Edge, uid domain.UserID) string {
	if e.User1 == uid {
		return string(e.User2)
	}
	return string(e.User1)
}
