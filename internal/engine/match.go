package engine

import (
	"math"
	"sort"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// MatchingConfig 是度约束（config["matching"]）。
type MatchingConfig struct {
	// BMin: 每人最少匹配数（下界显式可行性检查，qodo #9）。
	BMin int
	// BMax: 每人最多匹配数（上界贪心强制；绑定 member 左侧，
	// 同集模式对称约束双方）。
	BMax int
	// PoolBMax: pool（右）侧度数上界；nil = 不限。
	PoolBMax *int
}

// BlendingConfig 是 embed/llm 分数混合权重（config["blending"]）。
type BlendingConfig struct {
	EmbedWeight float64
	LLMWeight   float64
}

// MatchOutcome 是 SolveMatch 的输出三件套。
type MatchOutcome struct {
	// Edges: 匹配边（按 (-final_weight, pair_id) 排序）。
	Edges []domain.Edge
	// MatchProb: 匹配概率矩阵（确定性匹配 → 0/1，[M][N]；
	// 同集（无向）匹配对称存储 prob[i][j] == prob[j][i]）。
	MatchProb domain.Matrix
	// EnvyReport: envy 公平性报告 + b_min 可行性字段。
	EnvyReport map[string]any
}

// SolveMatch NSW 匹配求解 + envy 公平性检查（match 阶段，纯函数）。
//
// 算法：按 NSW 分数（双向偏好几何平均 sqrt(pref_lr·pref_rl)）降序的
// 确定性贪心 b-matching（平局取 (i,j) 字典序）——全局互惠最优意图。
//
// 边界（spec/05-boundaries.md）：
//   - §7 度约束 BMax 绑定 member（左）侧；PoolBMax 可选绑定 pool 侧；
//     同集（left == right）退化为无向图：BMax 对称约束双方。
//   - §3 未打分候选已在 pre_matrix 用 embed 权重兜底。
//   - BMin 不可满足时显式报告（b_min_violations），绝不静默吞掉；
//     由调用方决定继续运行 / 报警 / 门禁阻断。
//
// 说明：match 阶段只见 PrefMatrix（无独立可分离的 embed/llm 分），
// 因此边的 final_weight 取 NSW 分数（blending 在 embed==llm==nsw 时
// 退化为 (w_embed + w_llm)·nsw）；归一化已在 score 阶段完成。
func SolveMatch(prefMatrix *domain.PrefMatrix, matching MatchingConfig, blending BlendingConfig) MatchOutcome {
	m := prefMatrix.M()
	n := prefMatrix.N()
	bMin := matching.BMin
	if bMin < 0 {
		bMin = 0
	}
	matchProb := domain.NewMatrixZeros(m, n)

	if m == 0 || n == 0 {
		report := emptyEnvy()
		attachBMinReport(report, prefMatrix, matchProb, bMin)
		return MatchOutcome{Edges: nil, MatchProb: matchProb, EnvyReport: report}
	}

	poolBMax := matching.PoolBMax
	sameSet := sameIDList(prefMatrix.LeftIDs, prefMatrix.RightIDs)
	// BMax <= 0 显式定义为"不限度数"（上界 m+n）——否则配置漏写/拼错
	// b_max 会静默产出零匹配，且 envy=0 看起来像"完美公平"（CodeRabbit）。
	// Python 基线同样默认 0 但无此防护；golden 配置显式 b_max=4，不受影响。
	bMax := matching.BMax
	if bMax <= 0 {
		bMax = m + n
	}

	var matchedPairs []pairIJ

	// 候选对按 NSW 分数降序（平局取 (i, j) 字典序）。
	type cand struct {
		nsw  float64
		i, j int
	}
	var candidates []cand
	if sameSet {
		// 同集（cohort/full）：general matching，i < j 无序对。
		for i := 0; i < m; i++ {
			for j := i + 1; j < n; j++ {
				nsw := nswScore(prefMatrix, i, j)
				if nsw <= 0 {
					continue
				}
				candidates = append(candidates, cand{nsw: nsw, i: i, j: j})
			}
		}
	} else {
		// 二部图（market/batch）：全有序对。
		for i := 0; i < m; i++ {
			for j := 0; j < n; j++ {
				nsw := nswScore(prefMatrix, i, j)
				if nsw <= 0 {
					continue
				}
				candidates = append(candidates, cand{nsw: nsw, i: i, j: j})
			}
		}
	}
	sort.SliceStable(candidates, func(a, b int) bool {
		x, y := candidates[a], candidates[b]
		if x.nsw != y.nsw {
			return x.nsw > y.nsw
		}
		if x.i != y.i {
			return x.i < y.i
		}
		return x.j < y.j
	})

	if sameSet {
		deg := make([]int, m)
		for _, c := range candidates {
			if deg[c.i] < bMax && deg[c.j] < bMax {
				matchedPairs = append(matchedPairs, pairIJ{c.i, c.j})
				matchProb[c.i][c.j] = 1
				matchProb[c.j][c.i] = 1 // 无向匹配：对称存储
				deg[c.i]++
				deg[c.j]++
			}
		}
	} else {
		leftDeg := make([]int, m)
		rightDeg := make([]int, n)
		for _, c := range candidates {
			leftOK := leftDeg[c.i] < bMax
			rightOK := poolBMax == nil || rightDeg[c.j] < *poolBMax
			if leftOK && rightOK {
				matchedPairs = append(matchedPairs, pairIJ{c.i, c.j})
				matchProb[c.i][c.j] = 1
				leftDeg[c.i]++
				rightDeg[c.j]++
			}
		}
	}

	edges := buildEdges(prefMatrix, matchedPairs, blending.EmbedWeight, blending.LLMWeight)
	envyReport := CheckEnvy(prefMatrix, matchProb)
	attachBMinReport(envyReport, prefMatrix, matchProb, bMin)
	return MatchOutcome{Edges: edges, MatchProb: matchProb, EnvyReport: envyReport}
}

// CheckEnvy 检查匹配结果中的 envy 公平性（own-best 语义）。
//
// 语义（与 Evaluate 的 envy 计数逐位一致，改语义 = 改 oracle）：
// 左节点 i 嫉妒 i2 ⟺ i2 的匹配集中存在 j2，使 pref_lr[i][j2]
// 严格大于 i 自己最优匹配的偏好值。右侧同构。配对计算复用
// evaluate.go 的 collectRowMatches / envyPairs，两侧口径不分叉。
func CheckEnvy(prefMatrix *domain.PrefMatrix, matchProb domain.Matrix) map[string]any {
	// 左侧：envier = 行；右侧行视角取转置（行 = right 侧）。
	leftMatches := collectRowMatches(matchProb)
	rightMatches := collectRowMatches(transpose(matchProb))

	leftEnvy := envyPairs(prefMatrix.PrefLeftToRight, leftMatches)
	rightEnvy := envyPairs(prefMatrix.PrefRightToLeft, rightMatches)

	leftList := make([]any, len(leftEnvy))
	for k, p := range leftEnvy {
		leftList[k] = []int{p[0], p[1]}
	}
	rightList := make([]any, len(rightEnvy))
	for k, p := range rightEnvy {
		rightList[k] = []int{p[0], p[1]}
	}
	return map[string]any{
		"left_envy_count":  len(leftEnvy),
		"right_envy_count": len(rightEnvy),
		"total_envy":       len(leftEnvy) + len(rightEnvy),
		"left":             leftList,
		"right":            rightList,
	}
}

// nswScore NSW 分数：双向偏好的几何平均 sqrt(pref_lr·pref_rl)。
//
// 非正乘积（含异号乘积的 NaN 风险）返回 0：Python 基线此处是
// math.sqrt 对负数直接抛 ValueError，Go 侧以 0 优雅降级（该候选
// 不参与匹配），golden 路径乘积恒正、逐位一致不受影响。NaN 若
// 放行会绕过调用侧 nsw <= 0 过滤并破坏排序的严格弱序（CodeRabbit）。
func nswScore(pm *domain.PrefMatrix, i, j int) float64 {
	a := pm.PrefLeftToRight[i][j]
	b := pm.PrefRightToLeft[j][i]
	p := a * b
	if p <= 0 || math.IsNaN(p) || math.IsInf(p, 0) {
		return 0
	}
	return math.Sqrt(p)
}

// pairIJ 是偏好矩阵中的有序位置对（左行 i，右列 j）。
type pairIJ struct{ i, j int }

// buildEdges 把匹配对构造成 Edge（final_weight = blending 混合；
// 因 match 只见偏好矩阵，embed/llm 均以 NSW 重建）。
func buildEdges(pm *domain.PrefMatrix, matchedPairs []pairIJ, wEmbed, wLLM float64) []domain.Edge {
	edges := make([]domain.Edge, 0, len(matchedPairs))
	for _, p := range matchedPairs {
		user1 := pm.LeftIDs[p.i]
		user2 := pm.RightIDs[p.j]
		aToB := pm.PrefLeftToRight[p.i][p.j]
		bToA := pm.PrefRightToLeft[p.j][p.i]
		nsw := nswScore(pm, p.i, p.j) // 与候选过滤同一实现，避免两处逻辑分叉
		finalWeight := (wEmbed + wLLM) * nsw
		edges = append(edges, domain.Edge{
			User1:        user1,
			User2:        user2,
			PairID:       domain.StablePairID(user1, user2),
			FinalWeight:  finalWeight,
			EmbedScore:   nsw,
			LLMScore:     nsw,
			LLMScoreAToB: &aToB,
			LLMScoreBToA: &bToA,
		})
	}
	sort.SliceStable(edges, func(a, b int) bool {
		if edges[a].FinalWeight != edges[b].FinalWeight {
			return edges[a].FinalWeight > edges[b].FinalWeight
		}
		return edges[a].PairID < edges[b].PairID
	})
	return edges
}

func emptyEnvy() map[string]any {
	return map[string]any{
		"left_envy_count":  0,
		"right_envy_count": 0,
		"total_envy":       0,
		"left":             []any{},
		"right":            []any{},
	}
}

// attachBMinReport 把 b_min 可行性字段附加进报告（原地修改）。
//
// 度数口径：member 侧 = 左节点（二部图）或全部节点（同集无向图，
// 对称存储下行和即总度数）。b_min <= 0 时不启用，但字段仍写入
// （报告 shape 稳定，便于下游消费）。
func attachBMinReport(report map[string]any, pm *domain.PrefMatrix, matchProb domain.Matrix, bMin int) {
	m := pm.M()

	var violations []string
	for i := 0; i < m; i++ {
		// 度数 = 行和（同集模式对称存储下行和即总度数；二部图
		// 模式即 member 侧度数——两种口径在此统一）。
		degree := 0
		for _, v := range matchProb[i] {
			if v > matchThreshold {
				degree++
			}
		}
		if degree < bMin {
			violations = append(violations, string(pm.LeftIDs[i]))
		}
	}
	if violations == nil {
		violations = []string{}
	}
	report["b_min"] = bMin
	report["b_min_violations"] = violations
	report["b_min_satisfied"] = len(violations) == 0
}

func sameIDList(a, b []domain.UserID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
