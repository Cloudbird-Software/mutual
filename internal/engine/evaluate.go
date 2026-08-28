package engine

import (
	"fmt"
	"math"

	"github.com/Cloudbird-Software/mutual/internal/domain"
)

// matchThreshold 是匹配判定的二值阈值：MatchProb 中值 > matchThreshold
// 视为已匹配（同集对称存储 / 二部图单向存储均适用）。匹配、envy 统计
// 与 b_min 度数统计共用同一口径（改口径 = 改 oracle）。
const matchThreshold = 0.5

// EvaluateInput 是 evaluate 阶段的输入。
type EvaluateInput struct {
	// Predictions: 每个场景的推荐列表（按优先级降序）。
	Predictions [][]string
	// GroundTruth: 每个场景的唯一正例（与 Predictions 等长，
	// 不等长返回契约错误——静默截断会产生错误指标，qodo #6）。
	GroundTruth []string
	// PrefMatrix / MatchProb: 可选；两者齐备且非空时计算 envy 计数。
	PrefMatrix *domain.PrefMatrix
	MatchProb  domain.Matrix
}

// Evaluate 计算 HR@1/3/5、NDCG@5（推荐质量）+ envy 计数（互惠公平）
// （evaluate 阶段，spec/03-oracles.md 的评测 Oracle）。
//
// 指标语义（复用 AgentRecBench）：
//   - rank = ground_truth 在 predictions 中的 1-indexed 位置（未命中 ∞）；
//   - HR@K = 命中数（rank ≤ K） / 场景数；
//   - NDCG@5 = mean(1/log2(rank+1))，单 ground-truth → IDCG = 1。
//
// envy 计数（own-best 语义，与 CheckEnvy 逐位一致）：对每个实体 i，
// 若其他实体拿到的某个选项严格优于 i 自己最优的匹配，i 嫉妒之。
func Evaluate(in EvaluateInput) (domain.EvaluationReport, error) {
	if len(in.Predictions) != len(in.GroundTruth) {
		return domain.EvaluationReport{}, &domain.ContractError{
			Field:  "predictions/ground_truth",
			Reason: "长度不一致：评测输入畸形，拒绝计算（qodo #6）",
		}
	}
	total := len(in.Predictions)
	if total == 0 {
		return domain.EvaluationReport{
			Metadata: map[string]any{"prediction_lengths": []any{}},
		}, nil
	}

	hits1, hits3, hits5 := 0, 0, 0
	ndcg := 0.0
	for k, preds := range in.Predictions {
		rank := rankOf(in.GroundTruth[k], preds)
		if rank <= 1 {
			hits1++
		}
		if rank <= 3 {
			hits3++
		}
		if rank <= 5 {
			hits5++
		}
		ndcg += ndcgAt5(rank)
	}

	leftEnvy, rightEnvy := 0, 0
	if in.PrefMatrix != nil && in.MatchProb != nil &&
		in.MatchProb.Rows() > 0 && in.MatchProb.Cols() > 0 && anyAboveHalf(in.MatchProb) {
		// 形状契约（CodeRabbit）：PrefMatrix 与 MatchProb 是两个独立字段，
		// 不配套时 envyCount 的 pref[i][own[0]] 会越界 panic——与
		// predictions/ground_truth 同一契约纪律：畸形输入拒绝计算。
		if in.PrefMatrix.M() != in.MatchProb.Rows() || in.PrefMatrix.N() != in.MatchProb.Cols() {
			return domain.EvaluationReport{}, &domain.ContractError{
				Field: "pref_matrix/match_prob",
				Reason: fmt.Sprintf("形状不一致：pref %d×%d vs match_prob %d×%d，envy 输入畸形，拒绝计算",
					in.PrefMatrix.M(), in.PrefMatrix.N(), in.MatchProb.Rows(), in.MatchProb.Cols()),
			}
		}
		leftEnvy = envyCount(in.PrefMatrix.PrefLeftToRight, in.MatchProb)
		// 右侧：转置视角（行 = right 侧，列 = left 侧）。
		transposed := transpose(in.MatchProb)
		rightEnvy = envyCount(in.PrefMatrix.PrefRightToLeft, transposed)
	}

	lengths := make([]any, total)
	for i, p := range in.Predictions {
		lengths[i] = len(p)
	}
	meta := map[string]any{"prediction_lengths": lengths}
	// 零匹配可见性（RT-2026-08 #27）：envy 计数只覆盖有匹配的节点
	// （own-best 语义），竞争中的零匹配受害者对 envy 门禁"失明"——
	// 这里按 MatchProb 行/列全零统计双侧零匹配人数，供运营侧监控
	// （与 b_min_violations 互补：b_min=0 的部署也可见）。
	if in.MatchProb != nil && in.MatchProb.Rows() > 0 && in.MatchProb.Cols() > 0 {
		zeroLeft, zeroRight := 0, 0
		for i := 0; i < in.MatchProb.Rows(); i++ {
			anyMatch := false
			for j := 0; j < in.MatchProb.Cols(); j++ {
				if in.MatchProb[i][j] > 0.5 {
					anyMatch = true
					break
				}
			}
			if !anyMatch {
				zeroLeft++
			}
		}
		for j := 0; j < in.MatchProb.Cols(); j++ {
			anyMatch := false
			for i := 0; i < in.MatchProb.Rows(); i++ {
				if in.MatchProb[i][j] > 0.5 {
					anyMatch = true
					break
				}
			}
			if !anyMatch {
				zeroRight++
			}
		}
		meta["zero_matched_left"] = zeroLeft
		meta["zero_matched_right"] = zeroRight
	}
	return domain.EvaluationReport{
		HRAt1:          float64(hits1) / float64(total),
		HRAt3:          float64(hits3) / float64(total),
		HRAt5:          float64(hits5) / float64(total),
		NDCGAt5:        ndcg / float64(total),
		EnvyCountLeft:  leftEnvy,
		EnvyCountRight: rightEnvy,
		TotalScenarios: total,
		Metadata:       meta,
	}, nil
}

// rankOf 返回 target 在 predictions 中的 1-indexed 位置；未命中 ∞。
func rankOf(target string, predictions []string) float64 {
	for i, p := range predictions {
		if p == target {
			return float64(i + 1)
		}
	}
	return math.Inf(1)
}

// ndcgAt5 单 ground-truth（IDCG=1）：rank ≤ 5 时 1/log2(rank+1)。
func ndcgAt5(rank float64) float64 {
	if rank <= 5 {
		return 1.0 / math.Log2(rank+1)
	}
	return 0
}

// envyCount 统计一侧的 envy 计数（own-best 语义，与 CheckEnvy 的
// 配对逻辑同一实现——两处口径不分叉）。
// pref 行 = envier 侧，列 = 被匹配侧；match_prob 与 pref 同形。
func envyCount(pref domain.Matrix, matchProb domain.Matrix) int {
	return len(envyPairs(pref, collectRowMatches(matchProb)))
}

// collectRowMatches 返回每行 i 上值 > matchThreshold 的列下标列表
// （右侧行视角传转置矩阵即可复用，见 Evaluate / CheckEnvy）。
func collectRowMatches(m domain.Matrix) [][]int {
	rows := m.Rows()
	matches := make([][]int, rows)
	for i := 0; i < rows; i++ {
		for j, v := range m[i] {
			if v > matchThreshold {
				matches[i] = append(matches[i], j)
			}
		}
	}
	return matches
}

// envyPairs 统计一侧的 envy 对（own-best 语义）：实体 i 嫉妒 i2 ⟺
// i2 的匹配集中存在选项严格优于 i 自己最优匹配的偏好值。返回 (i, i2)
// 有序对，外层按 envier 升序、内层按被嫉妒者升序（与配对计数共用）。
func envyPairs(pref domain.Matrix, matches [][]int) [][2]int {
	var pairs [][2]int
	for i, own := range matches {
		if len(own) == 0 {
			continue
		}
		ownBest := pref[i][own[0]]
		for _, j := range own {
			if pref[i][j] > ownBest {
				ownBest = pref[i][j]
			}
		}
		for iPrime, other := range matches {
			if iPrime == i || len(other) == 0 {
				continue
			}
			envied := false
			for _, j := range other {
				if pref[i][j] > ownBest {
					envied = true
					break
				}
			}
			if envied {
				pairs = append(pairs, [2]int{i, iPrime})
			}
		}
	}
	return pairs
}

func anyAboveHalf(m domain.Matrix) bool {
	for _, row := range m {
		for _, v := range row {
			if v > matchThreshold {
				return true
			}
		}
	}
	return false
}

func transpose(m domain.Matrix) domain.Matrix {
	rows, cols := m.Rows(), m.Cols()
	t := domain.NewMatrixZeros(cols, rows)
	for i := 0; i < rows; i++ {
		for j := 0; j < cols; j++ {
			t[j][i] = m[i][j]
		}
	}
	return t
}
